package lockwaiter

import (
	"sync"
	"testing"
	"time"

	"github.com/ngaut/log"
	. "github.com/pingcap/check"
	deadlockPb "github.com/pingcap/kvproto/pkg/deadlock"
)

func TestT(t *testing.T) {
	TestingT(t)
}

var _ = Suite(&testLockwaiter{})

type testLockwaiter struct{}

func (t *testLockwaiter) TestLockwaiterBasic(c *C) {
	mgr := NewManager()

	mgr.NewWaiter(1, 2, 100, 10)

	// basic check queue and waiter
	q := mgr.waitingQueues[2]
	c.Assert(q, NotNil)
	waiter := q.waiters[0]
	c.Assert(waiter.startTS, Equals, uint64(1))
	c.Assert(waiter.LockTS, Equals, uint64(2))
	c.Assert(waiter.KeyHash, Equals, uint64(100))

	// check ready waiters
	keyHash := make([]uint64, 0, 10)
	keyHash = append(keyHash, 100)
	rdyWaiters, remainSize := q.getReadyWaiters(keyHash)
	rdyWaiter := rdyWaiters[0]
	c.Assert(remainSize, Equals, 0)
	c.Assert(rdyWaiter.startTS, Equals, uint64(1))
	c.Assert(rdyWaiter.LockTS, Equals, uint64(2))
	c.Assert(rdyWaiter.KeyHash, Equals, uint64(100))
	q.waiters = rdyWaiters

	// basic wake up test
	mgr.WakeUp(2, 222, keyHash)
	res := <-waiter.ch
	c.Assert(res.CommitTS, Equals, uint64(222))
	c.Assert(len(q.waiters), Equals, 0)
	q = mgr.waitingQueues[2]
	// verify queue deleted from map
	c.Assert(q, IsNil)

	// basic wake up for deadlock test
	waiter = mgr.NewWaiter(3, 4, 300, 10)
	resp := &deadlockPb.DeadlockResponse{}
	resp.Entry.Txn = 3
	resp.Entry.WaitForTxn = 4
	resp.Entry.KeyHash = 300
	resp.DeadlockKeyHash = 30192
	mgr.WakeUpForDeadlock(resp)
	res = <-waiter.ch
	c.Assert(res.DeadlockResp, NotNil)
	c.Assert(res.DeadlockResp.Entry.Txn, Equals, uint64(3))
	c.Assert(res.DeadlockResp.Entry.WaitForTxn, Equals, uint64(4))
	c.Assert(res.DeadlockResp.Entry.KeyHash, Equals, uint64(300))
	c.Assert(res.DeadlockResp.DeadlockKeyHash, Equals, uint64(30192))
	q = mgr.waitingQueues[4]
	// verify queue deleted from map
	c.Assert(q, IsNil)
}

func (t *testLockwaiter) TestLockwaiterConcurrent(c *C) {
	mgr := NewManager()
	wg := &sync.WaitGroup{}
	endWg := &sync.WaitGroup{}
	waitForTxn := uint64(100)
	commitTs := uint64(199)
	deadlockKeyHash := uint64(299)
	numbers := uint64(10)
	for i := uint64(0); i < numbers; i++ {
		wg.Add(1)
		endWg.Add(1)
		go func(num uint64) {
			defer endWg.Done()
			waiter := mgr.NewWaiter(num, waitForTxn, num*10, 100*time.Millisecond)
			// i == numbers - 1 use CleanUp Waiter and the results will be timeout
			if num == numbers-1 {
				mgr.CleanUp(waiter)
				wg.Done()
				res := waiter.Wait()
				c.Assert(res.Position, Equals, WaitTimeout)
				c.Assert(res.CommitTS, Equals, uint64(0))
				c.Assert(res.DeadlockResp, IsNil)
			} else {
				wg.Done()
				res := waiter.Wait()
				// even woken up by commit
				if num%2 == 0 {
					c.Assert(res.CommitTS, Equals, commitTs)
				} else {
					// odd woken up by deadlock
					c.Assert(res.DeadlockResp, NotNil)
					c.Assert(res.DeadlockResp.DeadlockKeyHash, Equals, deadlockKeyHash)
				}
			}
		}(i)
	}
	wg.Wait()
	keyHashes := make([]uint64, 0, 4)
	resp := &deadlockPb.DeadlockResponse{}
	for i := uint64(0); i < numbers; i++ {
		keyHashes = keyHashes[:0]
		if i%2 == 0 {
			log.Infof("wakeup i=%v", i)
			mgr.WakeUp(waitForTxn, commitTs, append(keyHashes, i*10))
		} else {
			log.Infof("deadlock wakeup i=%v", i)
			resp.DeadlockKeyHash = deadlockKeyHash
			resp.Entry.Txn = i
			resp.Entry.WaitForTxn = waitForTxn
			resp.Entry.KeyHash = i * 10
			mgr.WakeUpForDeadlock(resp)
		}
	}
	endWg.Wait()
}
