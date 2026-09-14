package mem

import (
	"bufio"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

const (
	procSelfCgroup    = "/proc/self/cgroup"
	procSelfMountInfo = "/proc/self/mountinfo"
)

type cgroupMembership struct {
	v2Path      string
	v2Found     bool
	memoryPath  string
	memoryFound bool
}

type cgroupMount struct {
	root       string
	mountPoint string
	fsType     string
	memory     bool
}

func cgroupAwareMemorySnapshot(host *MemorySnapshot, hostErr error, readFile memoryFileReader) (MemorySnapshot, error) {
	cgroup, found, err := readCgroupMemory(readFile)
	if err == nil && found {
		return combineMemorySnapshots(host, cgroup), nil
	}
	if err != nil {
		return MemorySnapshot{}, errors.Join(hostErr, fmt.Errorf("read cgroup memory: %w", err))
	}
	if host != nil {
		return *host, nil
	}
	if hostErr != nil {
		return MemorySnapshot{}, hostErr
	}
	return MemorySnapshot{}, errors.New("memory information is unavailable")
}

func readCgroupMemory(readFile memoryFileReader) (cgroupMemory, bool, error) {
	cgroupData, err := readFile(procSelfCgroup)
	if err != nil {
		return cgroupMemory{}, false, err
	}
	mountInfo, err := readFile(procSelfMountInfo)
	if err != nil {
		return cgroupMemory{}, false, err
	}
	membership, err := parseCgroupMembership(string(cgroupData))
	if err != nil {
		return cgroupMemory{}, false, err
	}
	mounts, err := parseCgroupMounts(string(mountInfo))
	if err != nil {
		return cgroupMemory{}, false, err
	}

	if membership.v2Found {
		for _, mount := range mounts {
			if mount.fsType != "cgroup2" {
				continue
			}
			base, ok := resolveCgroupPath(mount, membership.v2Path)
			if !ok {
				continue
			}
			return readCgroupHierarchy(readFile, base, mount.mountPoint, "memory.max", "memory.current", "max", MemorySourceCgroupV2)
		}
		if !membership.memoryFound {
			return cgroupMemory{}, false, errors.New("cgroup v2 membership found without a matching mount")
		}
	}

	if membership.memoryFound {
		for _, mount := range mounts {
			if mount.fsType != "cgroup" || !mount.memory {
				continue
			}
			base, ok := resolveCgroupPath(mount, membership.memoryPath)
			if !ok {
				continue
			}
			return readCgroupHierarchy(readFile, base, mount.mountPoint, "memory.limit_in_bytes", "memory.usage_in_bytes", "", MemorySourceCgroupV1)
		}
		return cgroupMemory{}, false, errors.New("cgroup v1 memory membership found without a matching mount")
	}

	return cgroupMemory{}, false, nil
}

func readCgroupHierarchy(
	readFile memoryFileReader,
	base string,
	mountPoint string,
	limitFile string,
	usedFile string,
	unlimitedValue string,
	source string,
) (cgroupMemory, bool, error) {
	base = path.Clean(base)
	mountPoint = path.Clean(mountPoint)
	if base != mountPoint && !strings.HasPrefix(base, mountPoint+"/") {
		return cgroupMemory{}, false, fmt.Errorf("cgroup path %q is outside mount point %q", base, mountPoint)
	}

	var result cgroupMemory
	found := false
	for current := base; ; current = path.Dir(current) {
		limitData, err := readFile(path.Join(current, limitFile))
		if err != nil {
			return cgroupMemory{}, false, err
		}
		if strings.TrimSpace(string(limitData)) != unlimitedValue {
			limit, err := parseMemoryValue(limitFile, limitData)
			if err != nil {
				return cgroupMemory{}, false, err
			}
			usedData, err := readFile(path.Join(current, usedFile))
			if err != nil {
				return cgroupMemory{}, false, err
			}
			used, err := parseMemoryValue(usedFile, usedData)
			if err != nil {
				return cgroupMemory{}, false, err
			}
			available := saturatingSub(limit, used)
			if !found {
				result = cgroupMemory{limit: limit, available: available, source: source}
				found = true
			} else {
				result.limit = min(result.limit, limit)
				result.available = min(result.available, available)
			}
		}
		if current == mountPoint {
			break
		}
	}
	return result, found, nil
}

func parseCgroupMembership(data string) (cgroupMembership, error) {
	var membership cgroupMembership
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			return cgroupMembership{}, fmt.Errorf("invalid cgroup entry %q", line)
		}
		if parts[0] == "0" && parts[1] == "" {
			membership.v2Path = parts[2]
			membership.v2Found = true
			continue
		}
		for _, controller := range strings.Split(parts[1], ",") {
			if controller == "memory" {
				membership.memoryPath = parts[2]
				membership.memoryFound = true
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return cgroupMembership{}, err
	}
	return membership, nil
}

func parseCgroupMounts(data string) ([]cgroupMount, error) {
	var mounts []cgroupMount
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		separator := -1
		for i, field := range fields {
			if field == "-" {
				separator = i
				break
			}
		}
		if len(fields) < 6 || separator < 6 || separator+3 >= len(fields) {
			return nil, fmt.Errorf("invalid mountinfo entry %q", line)
		}
		fsType := fields[separator+1]
		if fsType != "cgroup" && fsType != "cgroup2" {
			continue
		}
		mount := cgroupMount{
			root:       unescapeMountInfoPath(fields[3]),
			mountPoint: unescapeMountInfoPath(fields[4]),
			fsType:     fsType,
		}
		if fsType == "cgroup" {
			for _, option := range strings.Split(fields[separator+3], ",") {
				if option == "memory" {
					mount.memory = true
					break
				}
			}
		}
		mounts = append(mounts, mount)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return mounts, nil
}

func resolveCgroupPath(mount cgroupMount, membershipPath string) (string, bool) {
	root := path.Clean(mount.root)
	membershipPath = path.Clean(membershipPath)
	var relative string
	switch {
	case membershipPath == root:
		relative = "."
	case root == "/":
		relative = strings.TrimPrefix(membershipPath, "/")
	case membershipPath == "/":
		// In a cgroup namespace, / names the root exposed at the mount point.
		relative = "."
	case strings.HasPrefix(membershipPath, root+"/"):
		relative = strings.TrimPrefix(membershipPath, root+"/")
	default:
		return "", false
	}
	return path.Join(mount.mountPoint, relative), true
}

func parseMemoryValue(name string, data []byte) (uint64, error) {
	value := strings.TrimSpace(string(data))
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s value %q: %w", name, value, err)
	}
	return parsed, nil
}

func unescapeMountInfoPath(value string) string {
	replacer := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return replacer.Replace(value)
}
