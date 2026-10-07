package schedules

// katoptra/nongnu mirrors Savannah's nongnu release tree, twice a day.
var _ = register(Job{Repo: "katoptra/nongnu", File: "sync.yml", Slots: S3 | S15})
