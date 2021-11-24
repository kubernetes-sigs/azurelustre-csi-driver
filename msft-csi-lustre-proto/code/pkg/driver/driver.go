package driver

type Driver struct {
	name    string
	version string
}

func GetDriver(name, version string) *Driver {
	return &Driver{
		name:    name,
		version: version,
	}
}
