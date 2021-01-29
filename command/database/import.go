package database

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/spf13/cobra"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"

	"github.com/craftcms/nitro/pkg/database"
	"github.com/craftcms/nitro/pkg/filetype"
	"github.com/craftcms/nitro/pkg/labels"
	"github.com/craftcms/nitro/pkg/pathexists"
	"github.com/craftcms/nitro/pkg/terminal"
)

var importExampleText = `  # import a sql file into a database
  nitro db import filename.sql

  # use a relative path
  nitro db import ~/Desktop/backup.sql

  # use an absolute path
  nitro db import /Users/oli/Desktop/backup.sql`

// importCommand is the command for creating new development environments
func importCommand(home string, docker client.CommonAPIClient, output terminal.Outputer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a database",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("database backup file path param missing")
			}

			return nil
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return []string{"sql", "gz", "zip", "dump"}, cobra.ShellCompDirectiveFilterFileExt
		},
		Example: importExampleText,
		RunE: func(cmd *cobra.Command, args []string) error {
			show, err := strconv.ParseBool(cmd.Flag("show-output").Value.String())
			if err != nil {
				// set to false
				show = false
			}

			// replace the relative path
			path := args[0]
			if strings.HasPrefix(path, "~") {
				path = strings.Replace(path, "~", home, 1)
			}

			// make sure the file exists
			if exists := pathexists.IsFile(path); !exists {
				return fmt.Errorf("unable to find file at %s", path)
			}

			// check if this is a known archive type for docker
			isArchive := archive.IsArchivePath(path)

			// check if this is a zip file
			var isZip bool
			kind, err := filetype.Determine(path)
			if err != nil {
				return err
			}

			switch kind {
			case "zip", "tar":
				isZip = true
			}

			// detect the type of backup if not compressed
			detected := ""
			if !isArchive && !isZip {
				output.Pending("detecting backup type")

				// open the file
				f, err := os.Open(path)
				if err != nil {
					return err
				}

				// determine the database engine
				detected, err = database.DetermineEngine(f.Name())
				if err != nil {
					return err
				}

				output.Done()

				output.Info("Detected", detected, "backup")
			}

			// add filters to show only the environment and database containers
			filter := filters.NewArgs()
			filter.Add("label", labels.Nitro)
			filter.Add("label", labels.Type+"=database")

			// if we detected the engine type, add the compatibility label to the filter
			switch detected {
			case "mysql":
				filter.Add("label", labels.DatabaseCompatibility+"=mysql")
			case "postgres":
				filter.Add("label", labels.DatabaseCompatibility+"=postgres")
			}

			// get a list of all the databases
			containers, err := docker.ContainerList(cmd.Context(), types.ContainerListOptions{Filters: filter})
			if err != nil {
				return err
			}

			// sort containers by the name
			sort.SliceStable(containers, func(i, j int) bool {
				return containers[i].Names[0] < containers[j].Names[0]
			})

			// get all of the containers as a list
			var options []string
			for _, c := range containers {
				options = append(options, strings.TrimLeft(c.Names[0], "/"))
			}

			// prompt the user for the engine to import the backup into
			var containerID string
			selected, err := output.Select(os.Stdin, "Select a database engine: ", options)
			if err != nil {
				return err
			}

			// set the container id
			containerID = containers[selected].ID
			if containerID == "" {
				return fmt.Errorf("unable to get the container")
			}

			// ask the user for the database to create
			db, err := output.Ask("Enter the database name", "", ":", nil)
			if err != nil {
				return err
			}

			// get the filename by itself
			_, filename := filepath.Split(path)

			output.Info("Preparing import…")

			var rdr io.Reader
			switch isArchive {
			case false:
				// read the file content
				content, err := ioutil.ReadFile(path)
				if err != nil {
					return err
				}

				// generate the reader
				rdr, err = archive.Generate(filename, string(content))
				if err != nil {
					return err
				}
			default:
				rdr, err = os.Open(path)
				if err != nil {
					return err
				}
			}

			output.Pending("uploading backup", filename)

			// copy the file into the container
			if err := docker.CopyToContainer(cmd.Context(), containerID, "/tmp", rdr, types.CopyToContainerOptions{}); err != nil {
				output.Warning()
				return err
			}

			containerPath := "/tmp/" + filename

			// wait for the file to exist
			waiting := true
			for waiting {
				_, err := docker.ContainerStatPath(cmd.Context(), containerID, containerPath)
				if err == nil {
					waiting = false
				}

				if !waiting {
					break
				}
			}

			// determine if the backup is to mysql or postgres and run the import file command
			var createCmd, importCmd []string
			switch detected {
			case "postgres":
				createCmd = []string{"psql", "--username=nitro", "--host=127.0.0.1", fmt.Sprintf(`-c CREATE DATABASE %s;`, db)}
				importCmd = []string{"psql", "--username=nitro", "--host=127.0.0.1", db, "--file", fmt.Sprintf(`/tmp/%s`, filename)}
			default:
				createCmd = []string{"mysql", "-uroot", "-pnitro", fmt.Sprintf(`-e CREATE DATABASE IF NOT EXISTS %s;`, db)}
				// https: //dev.mysql.com/doc/refman/8.0/en/mysql-command-options.html
				importCmd = []string{"mysql", "-unitro", "-pnitro", db, fmt.Sprintf(`-e source /tmp/%s`, filename)}
			}

			// create the database
			if _, err := execCreate(cmd.Context(), docker, containerID, createCmd, show); err != nil {
				output.Warning()
				return fmt.Errorf("unable to create the database, %w", err)
			}

			// create the exec for create
			createExec, err := docker.ContainerExecCreate(cmd.Context(), containerID, types.ExecConfig{
				AttachStdout: true,
				AttachStderr: true,
				AttachStdin:  true,
				Tty:          false,
				Cmd:          createCmd,
			})
			if err != nil {
				return err
			}

			// attach to the container
			createResp, err := docker.ContainerExecAttach(cmd.Context(), createExec.ID, types.ExecStartCheck{
				Tty: false,
			})
			if err != nil {
				return err
			}
			defer createResp.Close()

			// should we display output?
			if show {
				// show the output to stdout and stderr
				if _, err := stdcopy.StdCopy(os.Stdout, os.Stderr, createResp.Reader); err != nil {
					return fmt.Errorf("unable to copy the output of container, %w", err)
				}
			}

			// start the exec
			if err := docker.ContainerExecStart(cmd.Context(), createExec.ID, types.ExecStartCheck{}); err != nil {
				return fmt.Errorf("unable to start the container, %w", err)
			}

			// wait for the container exec to complete
			createWaiting := true
			for createWaiting {
				resp, err := docker.ContainerExecInspect(cmd.Context(), createExec.ID)
				if err != nil {
					return err
				}

				createWaiting = resp.Running
			}

			// create the exec for import
			importExec, err := docker.ContainerExecCreate(cmd.Context(), containerID, types.ExecConfig{
				AttachStdout: true,
				AttachStderr: true,
				AttachStdin:  true,
				Tty:          true,
				Cmd:          importCmd,
			})
			if err != nil {
				return err
			}

			// attach to the container
			importResp, err := docker.ContainerExecAttach(cmd.Context(), importExec.ID, types.ExecStartCheck{
				Tty: false,
			})
			if err != nil {
				return err
			}
			defer importResp.Close()

			// should we display output?
			if show {
				// show the output to stdout and stderr
				if _, err := stdcopy.StdCopy(os.Stdout, os.Stderr, importResp.Reader); err != nil {
					return fmt.Errorf("unable to copy the output of container, %w", err)
				}
			}

			// start the exec
			if err := docker.ContainerExecStart(cmd.Context(), importExec.ID, types.ExecStartCheck{}); err != nil {
				return fmt.Errorf("unable to start the container, %w", err)
			}

			// wait for the container exec to complete
			importWaiting := true
			for importWaiting {
				resp, err := docker.ContainerExecInspect(cmd.Context(), importExec.ID)
				if err != nil {
					return err
				}

				importWaiting = resp.Running
			}

			output.Done()

			output.Info("Import successful 💪")

			return nil
		},
	}

	cmd.Flags().Bool("show-output", false, "show debug from import")

	return cmd
}
