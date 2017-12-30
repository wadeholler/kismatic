package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/apprenda/kismatic/pkg/install"
	"github.com/apprenda/kismatic/pkg/provision"

	"github.com/spf13/cobra"
)

// NewCmdProvision creates a new provision command
func NewCmdProvision(in io.Reader, out io.Writer, opts *installOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provision",
		Short: "provision your Kubernetes cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			fp := &install.FilePlanner{File: opts.planFilename}
			plan, err := fp.Read()
			if err != nil {
				return fmt.Errorf("unable to read plan file: %v", err)
			}
			path, err := os.Getwd()
			if err != nil {
				return err
			}

			tf := provision.AnyTerraform{
				Output:          out,
				BinaryPath:      filepath.Join(path, "terraform/bin/terraform"),
				KismaticVersion: install.KismaticVersion.String(),
				ProvidersDir:    filepath.Join(path, "terraform", "providers"),
				StateDir:        filepath.Join(path, assetsFolder),
			}

			envVars, err := tf.GetExpectedEnvVars(plan.Provisioner.Provider)
			if err != nil {
				return err
			}

			// This is a little awkward... we need to get the env vars defined
			// by the user to pass them to the provisioner, and then the
			// provisioner sets them again. Not sure if there is a way
			// around it though, as the provisioner needs to be able to work in both
			// the daemon and CLI scenario
			for optionName, envVarName := range envVars {
				value := os.Getenv(envVarName)
				if value == "" {
					return fmt.Errorf("environment variable %q must be set", envVarName)
				}
				plan.Provisioner.Options[optionName] = value
			}

			updatedPlan, err := tf.Provision(*plan)
			if err != nil {
				return err
			}
			if err := fp.Write(updatedPlan); err != nil {
				return fmt.Errorf("error writing updated plan file to %s: %v", opts.planFilename, err)
			}
			return nil
		},
	}
	return cmd
}

// NewCmdDestroy creates a new destroy command
func NewCmdDestroy(in io.Reader, out io.Writer, opts *installOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "destroy your provisioned cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			fp := &install.FilePlanner{File: opts.planFilename}
			plan, err := fp.Read()
			if err != nil {
				return fmt.Errorf("unable to read plan file: %v", err)
			}
			path, err := os.Getwd()
			if err != nil {
				return err
			}
			tf := provision.AnyTerraform{
				Output:          out,
				BinaryPath:      filepath.Join(path, "terraform/bin/terraform"),
				KismaticVersion: install.KismaticVersion.String(),
				ProvidersDir:    filepath.Join(path, "terraform", "providers"),
				StateDir:        filepath.Join(path, "terraform", "clusters"),
			}

			envVars, err := tf.GetExpectedEnvVars(plan.Provisioner.Provider)
			if err != nil {
				return err
			}

			// This is a little awkward... we need to get the env vars defined
			// by the user to pass them to the provisioner, and then the
			// provisioner sets them again. Not sure if there is a way
			// around it though, as the provisioner needs to be able to work in both
			// the daemon and CLI scenario
			for optionName, envVarName := range envVars {
				value := os.Getenv(envVarName)
				if value == "" {
					return fmt.Errorf("environment variable %q must be set", envVarName)
				}
				plan.Provisioner.Options[optionName] = value
			}

			return tf.Destroy(plan.Cluster.Name)
		},
	}
	return cmd
}
