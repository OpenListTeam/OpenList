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
	a.OwnerID, a.Path = "u1", "/movie.mkv"
	first, reused, err := s.SubmitLimited(a)
	if err != nil || reused {
		t.Fatalf("first submit: reused=%v err=%v", reused, err)
	}

	b := NewJob()
	b.OwnerID, b.Path = "u1", "/movie.mkv"
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
	c.OwnerID, c.Path = "u2", "/movie.mkv"
	third, reused, err := s.SubmitLimited(c)
	if err != nil || reused || third.ID == first.ID {
		t.Fatalf("other user must get own job: reused=%v err=%v", reused, err)
	}
}

func TestSubmitLimitedPerUserQuota(t *testing.T) {
	s := newTestScheduler()
	for i := 0; i < MaxActiveJobsPerUser; i++ {
		j := NewJob()
		j.OwnerID, j.Path = "u7", fmt.Sprintf("/f%d.mkv", i)
		if _, _, err := s.SubmitLimited(j); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	over := NewJob()
	over.OwnerID, over.Path = "u7", "/over.mkv"
	if _, _, err := s.SubmitLimited(over); !errors.Is(err, ErrUserQuota) {
		t.Fatalf("want ErrUserQuota, got %v", err)
	}
	// 其它用户不受影响
	other := NewJob()
	other.OwnerID, other.Path = "u8", "/other.mkv"
	if _, _, err := s.SubmitLimited(other); err != nil {
		t.Fatalf("other user blocked: %v", err)
	}
}

// Capacity 的判定必须与 SubmitLimited 一致：handler 靠它在启动 ffprobe 前预检配额。
func TestCapacityMatchesSubmitLimitedQuota(t *testing.T) {
	s := newTestScheduler()
	if err := s.Capacity("cap-user"); err != nil {
		t.Fatalf("empty scheduler should have capacity: %v", err)
	}
	for i := 0; i < MaxActiveJobsPerUser; i++ {
		j := NewJob()
		j.OwnerID, j.Path = "cap-user", fmt.Sprintf("/cap%d.mkv", i)
		if _, _, err := s.SubmitLimited(j); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	if err := s.Capacity("cap-user"); !errors.Is(err, ErrUserQuota) {
		t.Fatalf("want ErrUserQuota, got %v", err)
	}
	// 其他用户不受影响
	if err := s.Capacity("cap-other"); err != nil {
		t.Fatalf("other user should have capacity: %v", err)
	}
}

func TestSubmitLimitedQueueCap(t *testing.T) {
	s := newTestScheduler()
	for i := 0; i < MaxPendingJobs; i++ {
		j := NewJob()
		// 每个任务不同用户，避免触发单用户上限
		j.OwnerID, j.Path = fmt.Sprintf("u%d", 1000+i), "/x.mkv"
		if _, _, err := s.SubmitLimited(j); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	j := NewJob()
	j.OwnerID, j.Path = "u99999", "/y.mkv"
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
			j.OwnerID, j.Path = "u5", fmt.Sprintf("/p%d.mkv", i)
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

// 重新激活（播放端请求 playlist / 切片时触发）也必须占配额，
// 否则可以先让任务闲置被取消，再统一激活来绕过单用户上限。
func TestReactivateHonorsUserQuota(t *testing.T) {
	s := newTestScheduler()
	for i := 0; i < MaxActiveJobsPerUser; i++ {
		j := NewJob()
		j.OwnerID, j.Path = "u9", fmt.Sprintf("/r%d.mkv", i)
		if _, _, err := s.SubmitLimited(j); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}

	// 构造一个"该用户此前创建、随后因闲置被取消"的任务
	idle := NewJob()
	idle.OwnerID, idle.Path = "u10", "/idle.mkv"
	sub, _, err := s.SubmitLimited(idle)
	if err != nil {
		t.Fatalf("submit idle: %v", err)
	}
	sub.OwnerID = "u9"
	s.Cancel(sub.ID)

	if _, rerr := s.Reactivate(sub.ID); !errors.Is(rerr, ErrUserQuota) {
		t.Fatalf("want ErrUserQuota, got %v", rerr)
	}

	// 释放一个活跃任务后应能重新激活
	for _, j := range s.ListJobs() {
		if j.Path != "/idle.mkv" && j.OwnerID == "u9" {
			s.Cancel(j.ID)
			break
		}
	}
	if _, rerr := s.Reactivate(sub.ID); rerr != nil {
		t.Fatalf("reactivate after freeing quota: %v", rerr)
	}
	if st := sub.GetStatus(); st != JobPending {
		t.Fatalf("status=%v, want pending", st)
	}
}

// 重新激活同样不能突破全局排队上限：否则可以先让任务闲置取消，再批量激活把队列顶爆。
func TestReactivateHonorsQueueLimit(t *testing.T) {
	s := newTestScheduler()

	// 先造一个随后被取消的任务（Cancel 会把它从 pending 中移除）
	idle := NewJob()
	idle.OwnerID, idle.Path = "q1", "/idle.mkv"
	sub, _, err := s.SubmitLimited(idle)
	if err != nil {
		t.Fatalf("submit idle: %v", err)
	}
	s.Cancel(sub.ID)

	// 用不同用户把全局队列填满（避开单用户配额）
	for i := 0; i < MaxPendingJobs; i++ {
		j := NewJob()
		j.OwnerID, j.Path = fmt.Sprintf("q%d", i+2), fmt.Sprintf("/q%d.mkv", i)
		if _, _, err := s.SubmitLimited(j); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}

	if _, rerr := s.Reactivate(sub.ID); !errors.Is(rerr, ErrQueueFull) {
		t.Fatalf("want ErrQueueFull, got %v", rerr)
	}
	if st := sub.GetStatus(); st != JobCancelled {
		t.Fatalf("status=%v, want cancelled (must not be requeued)", st)
	}
}

func TestValidSourceInput(t *testing.T) {
	ok := []string{
		"/data/movies/a.mkv",
		"http://127.0.0.1:5244/d/a.mkv?sign=x",
		"https://example.com/d/a.mkv",
		`C:\media\a.mkv`,
		"C:/media/a.mkv",
		// 文件名里的冒号很常见，不能被当成协议前缀
		"/mnt/media/Star Wars: Episode IV.mkv",
		"/data/Spider-Man: Homecoming (2017).mkv",
		`D:\Movies\Star Wars: IV.mkv`,
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
	// 远程输入不应包含 file / crypto：HTTP 源用不到，留着等于给构造过的播放列表
	// 留了一条读本地文件的通道
	for _, banned := range []string{"concat", "subfile", "data", "pipe", "rtmp", "ftp", "file", "crypto"} {
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
