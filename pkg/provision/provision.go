package provision

import "github.com/apprenda/kismatic/pkg/install"

// Provisioner is responsible for creating and destroying infrastructure for
// a given cluster.
type Provisioner interface {
	Provision(install.Plan) (*install.Plan, error)
	Destroy(string) error
}
