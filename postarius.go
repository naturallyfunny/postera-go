package postera

type Postarius struct {
	registry Registry
	enqueuer Enqueuer
}

func New(registry Registry, enqueuer Enqueuer) *Postarius {
	return &Postarius{registry: registry, enqueuer: enqueuer}
}
