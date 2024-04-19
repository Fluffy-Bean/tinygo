//go:build scheduler.tasks

package task

import (
	"runtime/interrupt"
	"unsafe"
)

//go:linkname runtimePanic runtime.runtimePanic
func runtimePanic(str string)

// Stack canary, to detect a stack overflow. The number is a random number
// generated by random.org. The bit fiddling dance is necessary because
// otherwise Go wouldn't allow the cast to a smaller integer size.
const stackCanary = uintptr(uint64(0x670c1333b83bf575) & uint64(^uintptr(0)))

// state is a structure which holds a reference to the state of the task.
// When the task is suspended, the registers are stored onto the stack and the stack pointer is stored into sp.
type state struct {
	// sp is the stack pointer of the saved state.
	// When the task is inactive, the saved registers are stored at the top of the stack.
	// Note: this should ideally be a unsafe.Pointer for the precise GC. The GC
	// will find the stack through canaryPtr though so it's not currently a
	// problem to store this value as uintptr.
	sp uintptr

	// canaryPtr points to the top word of the stack (the lowest address).
	// This is used to detect stack overflows.
	// When initializing the goroutine, the stackCanary constant is stored there.
	// If the stack overflowed, the word will likely no longer equal stackCanary.
	canaryPtr *uintptr
}

// currentTask is the current running task, or nil if currently in the scheduler.
var currentTask *Task

// Current returns the current active task.
func Current() *Task {
	return currentTask
}

// Pause suspends the current task and returns to the scheduler.
// This function may only be called when running on a goroutine stack, not when running on the system stack or in an interrupt.
func Pause() {
	// Check whether the canary (the lowest address of the stack) is still
	// valid. If it is not, a stack overflow has occurred.
	if *currentTask.state.canaryPtr != stackCanary {
		runtimePanic("goroutine stack overflow")
	}
	if interrupt.In() {
		runtimePanic("blocked inside interrupt")
	}
	currentTask.state.pause()
}

//export tinygo_pause
func pause() {
	Pause()
}

// Resume the task until it pauses or completes.
// This may only be called from the scheduler.
func (t *Task) Resume() {
	currentTask = t
	t.gcData.swap()
	t.state.resume()
	t.gcData.swap()
	currentTask = nil
}

// initialize the state and prepare to call the specified function with the specified argument bundle.
func (s *state) initialize(fn uintptr, args unsafe.Pointer, stackSize uintptr) {
	// Create a stack.
	stack := runtime_alloc(stackSize, nil)

	// Set up the stack canary, a random number that should be checked when
	// switching from the task back to the scheduler. The stack canary pointer
	// points to the first word of the stack. If it has changed between now and
	// the next stack switch, there was a stack overflow.
	s.canaryPtr = (*uintptr)(stack)
	*s.canaryPtr = stackCanary

	// Get a pointer to the top of the stack, where the initial register values
	// are stored. They will be popped off the stack on the first stack switch
	// to the goroutine, and will start running tinygo_startTask (this setup
	// happens in archInit).
	r := (*calleeSavedRegs)(unsafe.Add(stack, stackSize-unsafe.Sizeof(calleeSavedRegs{})))

	// Invoke architecture-specific initialization.
	s.archInit(r, fn, args)
}

//export tinygo_swapTask
func swapTask(oldStack uintptr, newStack *uintptr)

// startTask is a small wrapper function that sets up the first (and only)
// argument to the new goroutine and makes sure it is exited when the goroutine
// finishes.
//
//go:extern tinygo_startTask
var startTask [0]uint8

//go:linkname runqueuePushBack runtime.runqueuePushBack
func runqueuePushBack(*Task)

//go:linkname runtime_alloc runtime.alloc
func runtime_alloc(size uintptr, layout unsafe.Pointer) unsafe.Pointer

// start creates and starts a new goroutine with the given function and arguments.
// The new goroutine is scheduled to run later.
func start(fn uintptr, args unsafe.Pointer, stackSize uintptr) {
	t := &Task{}
	t.state.initialize(fn, args, stackSize)
	runqueuePushBack(t)
}

// OnSystemStack returns whether the caller is running on the system stack.
func OnSystemStack() bool {
	// If there is not an active goroutine, then this must be running on the system stack.
	return Current() == nil
}
