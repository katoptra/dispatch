package schedules

// katoptra/dropbox is a mirror of a Dropbox account in Proton Drive. Each run starts the
// next run in a chain until all batches are done. Thus, the scheduler dispatches it daily,
// and that starts the runs for all batches.
var _ = register(Job{Repo: "katoptra/dropbox", File: "sync.yml", Slots: Overnight})
