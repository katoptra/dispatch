package schedules

// katoptra/gnu is a mirror of GNU's release tree. The scheduler starts it twice a
// day.
var _ = register(Job{Repo: "katoptra/gnu", File: "sync.yml", Slots: S3 | S15})
