package schedules

// katoptra/gnu mirrors GNU's release tree from a secondary, twice a day.
var _ = register(Job{Repo: "katoptra/gnu", File: "sync.yml", Slots: S3 | S15})
