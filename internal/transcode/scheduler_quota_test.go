package transcode

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func newTestScheduler() *Scheduler {
	return NewScheduler(NewWorkerRegistry())
}

func TestSubmitLimitedReusesSameUserPath(t *testing.T) {
	s := newTestScheduler()
	a := NewJob()
	a.OwnerID, a.Path = 1, "/movie.mkv"
	first, reused, err := s.SubmitLimited(a)
	if err != nil || reused {
		t.Fatalf("first submit: reused=%v err=%v", reused, err)
	}

	b := NewJob()
	b.OwnerID, b.Path = 1, "/movie.mkv"
	second, reused, err := s.SubmitLimited(b)
	if err != nil || !reused {
		t.Fatalf("second submit should reuse: reused=%v err=%v", reused, err)
	}
	if second.ID != first.ID {
		t.Fatalf("reuse returned a different job: %s vs %s", second.ID, first.ID)
	}
	if got := len(s.ListJobs()); got != 1 {
		t.Fatalf("duplicate job queued, jobs=%d", got)
	}

	// 其他用户不应复用别人的任务（callback_token 属于发起者）
	c := NewJob()
	c.OwnerID, c.Path = 2, "/movie.mkv"
	third, reused, err := s.SubmitLimited(c)
	if err != nil || reused || third.ID == first.ID {
		t.Fatalf("other user must get own job: reused=%v err=%v", reused, err)
	}
}

func TestSubmitLimitedPerUserQuota(t *testing.T) {
	s := newTestScheduler()
	for i := 0; i < MaxActiveJobsPerUser; i++ {
		j := NewJob()
		j.OwnerID, j.Path = 7, fmt.Sprintf("/f%d.mkv", i)
		if _, _, err := s.SubmitLimited(j); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	over := NewJob()
	over.OwnerID, over.Path = 7, "/over.mkv"
	if _, _, err := s.SubmitLimited(over); !errors.Is(err, ErrUserQuota) {
		t.Fatalf("want ErrUserQuota, got %v", err)
	}
	// 其它用户不受影响
	other := NewJob()
	other.OwnerID, other.Path = 8, "/other.mkv"
	if _, _, err := s.SubmitLimited(other); err != nil {
		t.Fatalf("other user blocked: %v", err)
	}
}

func TestSubmitLimitedQueueCap(t *testing.T) {
	s := newTestScheduler()
	for i := 0; i < MaxPendingJobs; i++ {
		j := NewJob()
		// 每个任务不同用户，避免触发单用户上限
		j.OwnerID, j.Path = uint(1000+i), "/x.mkv"
		if _, _, err := s.SubmitLimited(j); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	j := NewJob()
	j.OwnerID, j.Path = 99999, "/y.mkv"
	if _, _, err := s.SubmitLimited(j); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("want ErrQueueFull, got %v", err)
	}
}

func TestSubmitLimitedConcurrentQuota(t *testing.T) {
	s := newTestScheduler()
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j := NewJob()
			j.OwnerID, j.Path = 5, fmt.Sprintf("/p%d.mkv", i)
			if _, _, err := s.SubmitLimited(j); err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if accepted != MaxActiveJobsPerUser {
		t.Fatalf("concurrent submits accepted=%d, want %d", accepted, MaxActiveJobsPerUser)
	}
}

func TestValidSourceInput(t *testing.T) {
	ok := []string{
		"/data/movies/a.mkv",
		"http://127.0.0.1:5244/d/a.mkv?sign=x",
		"https://example.com/d/a.mkv",
		`C:\media\a.mkv`,
		"C:/media/a.mkv",
	}
	for _, s := range ok {
		if !ValidSourceInput(s) {
			t.Errorf("%q should be accepted", s)
		}
	}
	bad := []string{
		"", "-i", "-f", "concat:/etc/passwd|/etc/hosts", "subfile,,start,0,end,0,,:/etc/passwd",
		"data:text/plain;base64,AAAA", "pipe:0", "rtmp://x/y", "ftp://x/y", "file:///etc/passwd",
		// 盘符后必须紧跟路径分隔符，否则不是合法的 Windows 绝对路径
		"C:foo",
	}
	for _, s := range bad {
		if ValidSourceInput(s) {
			t.Errorf("%q should be rejected", s)
		}
	}
}

func TestProtocolWhitelistFor(t *testing.T) {
	if got := ProtocolWhitelistFor("/data/a.mkv"); got != "file" {
		t.Errorf("local = %q", got)
	}
	if got := ProtocolWhitelistFor("HTTPS://x/y"); got == "file" || got == "" {
		t.Errorf("remote = %q", got)
	}
	for _, banned := range []string{"concat", "subfile", "data", "pipe", "rtmp", "ftp"} {
		if contains := func(s string) bool {
			for _, p := range splitComma(s) {
				if p == banned {
					return true
				}
			}
			return false
		}(ProtocolWhitelistFor("http://x/y")); contains {
			t.Errorf("remote whitelist must not include %s", banned)
		}
	}
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}
