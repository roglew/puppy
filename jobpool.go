package main

import (
	"fmt"
	"sync"
)

// An interface which represents a job to be done by the pool
type Job interface {
	Run() // Start the job
	Abort() // Abort any work that needs to be completed and close the DoneChannel
	DoneChannel() chan struct{} // Returns a channel that is closed when the job is done
}

// An interface which represents a pool of workers doing jobs
type JobPool struct {
	MaxThreads int

	jobQueue chan Job
	jobQueueDone chan struct{}
	jobQueueAborted chan struct{}
	jobQueueShutDown chan struct{}
}

func NewJobPool(maxThreads int) (*JobPool) {
	q := JobPool {
		MaxThreads: maxThreads,
		jobQueue: make(chan Job),
		jobQueueDone: make(chan struct{}),      // Closing will shut down workers and reject any incoming work
		jobQueueAborted: make(chan struct{}),   // Closing tells workers to abort
		jobQueueShutDown: make(chan struct{}),  // Closed when all workers are shut down
	}

	return &q
}

func (q *JobPool) RunJob(j Job) error {
	select {
	case <-q.jobQueueDone:
		return fmt.Errorf("job queue closed")
	default:
	}

	q.jobQueue <- j
	return nil
}

func (q *JobPool) Run() {
	if q.MaxThreads > 0 {
		// Create pool of routines that read from the queue and run jobs
		var w sync.WaitGroup
		for i:=0; i<q.MaxThreads; i++ {
			w.Add(1)
			go func() {
				defer w.Done()
				for {
					select {
					case job := <-q.jobQueue:
						go func() {
							job.Run()
						}()

						select {
						case <-job.DoneChannel(): // The job finishes normally
						case <-q.jobQueueAborted: // We have to abort the job
							job.Abort()         // Tell the job to abort
							<-job.DoneChannel() // Wait for the job to abort
						}
					case <-q.jobQueueDone:
						// We're done and out of jobs, quit
						close(q.jobQueueShutDown) // Flag that the workers quit
						return
					}
				}
			}()
		}

		w.Wait() // Wait for workers to quit
		close(q.jobQueueShutDown) // Flag that all workers quit
	} else {
		// Create a thread any time we pull something out of the job queue
		for {
			select {
			case job := <-q.jobQueue:
				go func() {
					go func() {
						job.Run()
					}()

					select {
					case <-job.DoneChannel(): // The job finishes normally
					case <-q.jobQueueAborted: // We have to abort the job
						job.Abort()         // Tell the job to abort
						<-job.DoneChannel() // Wait for the job to abort
					}
				}()
			case <-q.jobQueueDone:
				close(q.jobQueueShutDown) // Flag that the workers quit
				return
			}
		}
	}
}

func (q *JobPool) Abort() {
	close(q.jobQueueDone) // Stop accepting jobs and tell the workers to quit
	close(q.jobQueueAborted) // Tell the workers to abort
	<-q.jobQueueShutDown  // Wait for all the workers to shut down
	close(q.jobQueue)     // Clean up the job queue
}

func (q *JobPool) CompleteAndClose() {
	close(q.jobQueueDone) // Stop accepting jobs and tell the workers to quit
	<-q.jobQueueShutDown  // Wait for all the workers to shut down
	close(q.jobQueue)     // Clean up the job queue
	close(q.jobQueueAborted) // Clean up abort channel
}
