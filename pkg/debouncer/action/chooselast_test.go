package action_test

import (
	"context"
	"errors"

	"github.com/owncloud/reva/v2/pkg/debouncer/action"
	"github.com/owncloud/reva/v2/pkg/debouncer/tasklist"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type DummyTask struct {
	Err  error
	Done bool
}

func (d *DummyTask) ExposeData() map[string]string {
	return nil
}

func (d *DummyTask) Execute(ctx context.Context) error {
	d.Done = true
	return d.Err
}

var _ = Describe("ChooseLast", func() {
	var cl action.Action

	BeforeEach(func() {
		cl = action.NewChooseLast()
	})

	Describe("RunTasks", func() {
		It("Choose last task", func() {
			ctx := context.Background()
			task1 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
			task2 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
			task3 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
			task4 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
			taskList := []tasklist.InternalTask{task1, task2, task3, task4}

			err := cl.RunTasks(ctx, taskList)
			Expect(err).To(Succeed())
			Expect(task1.OriginalTask.(*DummyTask).Done).To(Equal(false))
			Expect(task2.OriginalTask.(*DummyTask).Done).To(Equal(false))
			Expect(task3.OriginalTask.(*DummyTask).Done).To(Equal(false))
			Expect(task4.OriginalTask.(*DummyTask).Done).To(Equal(true))
		})

		It("Last task fails", func() {
			ctx := context.Background()
			terr := errors.New("oopsie!!")
			task1 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
			task2 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
			task3 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{})
			task4 := tasklist.NewInternalTaskFromTask(ctx, &DummyTask{Err: terr})
			taskList := []tasklist.InternalTask{task1, task2, task3, task4}

			err := cl.RunTasks(ctx, taskList)
			Expect(err).To(Equal(terr))
			Expect(task1.OriginalTask.(*DummyTask).Done).To(Equal(false))
			Expect(task2.OriginalTask.(*DummyTask).Done).To(Equal(false))
			Expect(task3.OriginalTask.(*DummyTask).Done).To(Equal(false))
			Expect(task4.OriginalTask.(*DummyTask).Done).To(Equal(true))
		})

		It("Empty task list fails", func() {
			taskList := []tasklist.InternalTask{}
			err := cl.RunTasks(context.Background(), taskList)
			Expect(err).To(Equal(action.ErrEmptyList))
		})

		It("Nil task list fails", func() {
			err := cl.RunTasks(context.Background(), nil)
			Expect(err).To(Equal(action.ErrEmptyList))
		})
	})
})
