package tasklist_test

import (
	"context"
	"sync"

	"github.com/owncloud/reva/v2/pkg/debouncer/tasklist"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type DummyTask struct {
	Err error
}

func (t *DummyTask) ExposeData() map[string]string {
	return nil
}

func (t *DummyTask) Execute(ctx context.Context) error {
	return t.Err
}

var _ = Describe("SliceWithLocks", func() {
	var swl tasklist.TaskList

	BeforeEach(func() {
		swl = tasklist.NewSliceWithLocks()
	})

	Describe("Only one goroutine", func() {
		It("Add to list successful", func() {
			task1 := tasklist.NewInternalTaskFromTask(context.Background(), &DummyTask{})
			Expect(swl.AddToList(task1)).To(BeTrue())
		})

		It("Add to list fails if frozen", func() {
			swl.Freeze()
			task1 := tasklist.NewInternalTaskFromTask(context.Background(), &DummyTask{})
			Expect(swl.AddToList(task1)).To(BeFalse())
		})

		It("Check not frozen initially", func() {
			Expect(swl.IsFrozen()).To(BeFalse())
		})

		It("Check frozen state after freeze", func() {
			swl.Freeze()
			Expect(swl.IsFrozen()).To(BeTrue())
		})

		It("ToSlice returns added tasks", func() {
			ctx := context.Background()
			task1 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
			task2 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
			swl.AddToList(task1)
			swl.AddToList(task2)

			swl.Freeze() // not needed, but recommended
			returnedList := swl.ToSlice()
			Expect(returnedList).To(HaveLen(2))
			Expect(returnedList[0]).To(Equal(task1))
			Expect(returnedList[1]).To(Equal(task2))
		})

		It("ToSlice won't be modified if not frozen", func() {
			ctx := context.Background()
			task1 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
			task2 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
			swl.AddToList(task1)
			swl.AddToList(task2)

			list1 := swl.ToSlice()
			task3 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
			Expect(swl.AddToList(task3)).To(BeTrue()) // not frozen -> adding is allowed
			Expect(list1).To(HaveLen(2))
			Expect(list1[0]).To(Equal(task1))
			Expect(list1[1]).To(Equal(task2))

			list2 := swl.ToSlice()
			Expect(list2).To(HaveLen(3))
			Expect(list2[0]).To(Equal(task1))
			Expect(list2[1]).To(Equal(task2))
			Expect(list2[2]).To(Equal(task3))
		})
	})

	Describe("Multiple goroutines", func() {
		It("AddToList can be used from multiple goroutines", func() {
			ctx := context.Background()
			var wg sync.WaitGroup
			wg.Go(func() {
				defer GinkgoRecover()
				task1 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
				Expect(swl.AddToList(task1)).To(BeTrue())
			})
			wg.Go(func() {
				defer GinkgoRecover()
				task1 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
				Expect(swl.AddToList(task1)).To(BeTrue())
			})
			wg.Go(func() {
				defer GinkgoRecover()
				task1 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
				Expect(swl.AddToList(task1)).To(BeTrue())
			})
			wg.Wait()
			Expect(swl.IsFrozen()).To(BeFalse())
			Expect(swl.ToSlice()).To(HaveLen(3))
		})

		It("ToSlice won't modify the backed list", func() {
			ctx := context.Background()
			task1 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
			Expect(swl.AddToList(task1)).To(BeTrue())

			var wg sync.WaitGroup
			wg.Go(func() {
				defer GinkgoRecover()

				list := swl.ToSlice()
				Expect(list).To(HaveLen(1))

				task2 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
				list = append(list, task2)
				Expect(list).To(HaveLen(2))
			})
			wg.Go(func() {
				defer GinkgoRecover()

				list := swl.ToSlice()
				Expect(list).To(HaveLen(1))

				task2 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
				list = append(list, task2)
				Expect(list).To(HaveLen(2))
			})
			wg.Wait()

			list := swl.ToSlice()
			Expect(list).To(HaveLen(1))
		})
	})
})
