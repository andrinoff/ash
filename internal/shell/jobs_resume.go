package shell

// LastStoppedJob returns the number of the newest stopped job, or 0. The
// line editor resumes it on ctrl-z when the command line is empty.
func (sh *Shell) LastStoppedJob() int {
	id := 0
	for _, j := range sh.shared.jobs.list() {
		if !j.disowned && j.state() == procStopped {
			id = j.id
		}
	}
	return id
}
