package policy_test

import (
	"sync"
	"time"

	"github.com/owncloud/reva/v2/pkg/debouncer/policy"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gleak"
)

var _ = Describe("TimedWithReset", func() {
	var tr policy.Policy

	BeforeEach(func() {
		tr = policy.NewTimedWithReset(1 * time.Second)
	})

	AfterEach(func() {
		Eventually(Goroutines).ShouldNot(HaveLeaked())
	})

	Describe("Only one goroutine", func() {
		It("Waiting channel closed after trigger", func() {
			Eventually(tr.WaitForTrigger()).WithTimeout(3 * time.Second).Should(BeClosed())
		})

		It("Immediately returns after channel closes", func() {
			Eventually(tr.WaitForTrigger()).WithTimeout(3 * time.Second).Should(BeClosed())

			t1 := time.Now()
			Eventually(tr.WaitForTrigger()).WithTimeout(3 * time.Second).Should(BeClosed())
			Expect(time.Now()).To(BeTemporally("~", t1)) // within 1ms of difference
		})

		It("MarkTaskAdded extends duration", func() {
			time.Sleep(700 * time.Millisecond)
			Expect(tr.MarkTaskAdded()).To(Equal(true))
			Consistently(tr.WaitForTrigger(), "1s").ShouldNot(BeClosed())
			Eventually(tr.WaitForTrigger()).WithTimeout(3 * time.Second).Should(BeClosed())
		})

		It("MarkTaskAdded fails if channel closed", func() {
			Eventually(tr.WaitForTrigger()).WithTimeout(3 * time.Second).Should(BeClosed())
			Expect(tr.MarkTaskAdded()).To(Equal(false))
		})
	})

	Describe("Multiple goroutines", func() {
		It("Two goroutines wait until channel closes", func() {
			// No goroutine gets stuck waiting indefinitely after the policy has triggered
			var wg sync.WaitGroup
			wg.Go(func() {
				defer GinkgoRecover()
				Eventually(tr.WaitForTrigger()).WithTimeout(3 * time.Second).Should(BeClosed())
			})
			wg.Go(func() {
				defer GinkgoRecover()
				Eventually(tr.WaitForTrigger()).WithTimeout(3 * time.Second).Should(BeClosed())
			})
			wg.Wait()
		})

		It("Second goroutine doesn't wait after channel close", func() {
			var wg sync.WaitGroup
			wg.Go(func() {
				defer GinkgoRecover()
				Eventually(tr.WaitForTrigger()).WithTimeout(3 * time.Second).Should(BeClosed())
			})
			wg.Wait()

			t1 := time.Now()
			Eventually(tr.WaitForTrigger()).WithTimeout(3 * time.Second).Should(BeClosed())
			Expect(time.Now()).To(BeTemporally("~", t1)) // within 1ms of difference
		})

		It("Goroutine keeps adding tasks", func() {
			var wg sync.WaitGroup
			wg.Go(func() {
				defer GinkgoRecover()
				// each 500ms add a task, up to 4
				for i := 0; i < 4; i++ {
					time.Sleep(500 * time.Millisecond)
					Expect(tr.MarkTaskAdded()).To(Equal(true))
				}
			})
			wg.Go(func() {
				defer GinkgoRecover()
				t1 := time.Now()
				Eventually(tr.WaitForTrigger()).WithTimeout(5 * time.Second).Should(BeClosed())
				// timer should reset each 0.5s until the 2s, no further resets so final time should be 3s
				Expect(time.Now()).To(BeTemporally("~", t1.Add(3*time.Second), 100*time.Millisecond)) // within 100ms of difference
			})
			wg.Wait()
		})
	})
})
