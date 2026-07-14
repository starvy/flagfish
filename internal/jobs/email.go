package jobs

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"

	"github.com/starvy/flagfish/internal/mail"
)

// SendEmail is enqueued inside the transaction whose outcome the email announces —
// a rolled-back registration un-enqueues its own verification mail. The message is
// fully composed at enqueue time; the worker only delivers it.
type SendEmail struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// Kind is the River job kind. It is stable across renames of the Go type; changing it
// orphans queued jobs.
func (SendEmail) Kind() string { return "email_send" }

func (SendEmail) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: river.QueueDefault}
}

// SendEmailWorker delivers queued mail. A failed send is returned, never swallowed:
// River owns the retries, and a swallowed error here is a player who never gets
// their reset link and no operator who knows why.
type SendEmailWorker struct {
	river.WorkerDefaults[SendEmail]
	Mailer mail.Mailer
}

func (w *SendEmailWorker) Work(ctx context.Context, job *river.Job[SendEmail]) error {
	// The body may carry a live one-time token; it must never end up in a log line.
	if err := w.Mailer.Send(ctx, job.Args.To, job.Args.Subject, job.Args.Body); err != nil {
		return fmt.Errorf("jobs: send email: %w", err)
	}
	return nil
}
