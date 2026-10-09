package schedules

// katoptra/gnu-alpha is a mirror of GNU's alpha release tree. The scheduler starts it twice
// a day.
var _ = register(Job{Repo: "katoptra/gnu-alpha", File: "sync.yml", Slots: S9 | S21})
