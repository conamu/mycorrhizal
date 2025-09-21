package mycorrizal

type Mycorrizal interface {
}

type mycorrizal struct{}

func New(cfg *Config) (Mycorrizal, error) {
	return &mycorrizal{}, nil
}
