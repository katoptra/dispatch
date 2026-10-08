package schedules

// katoptra/gnu is a mirror of GNU's release tree from a secondary mirror, twice a day.
var _ = register(Job{Repo: "katoptra/gnu", File: "sync.yml", Slots: S3 | S15})
