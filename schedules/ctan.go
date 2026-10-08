package schedules

// katoptra/ctan is a mirror of CTAN in R2.
var _ = register(Job{Repo: "katoptra/ctan", File: "sync.yml", Slots: Hourly})
