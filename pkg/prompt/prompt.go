package prompt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/craftcms/nitro/pkg/config"
	"github.com/craftcms/nitro/pkg/labels"
	"github.com/craftcms/nitro/pkg/phpversions"
	"github.com/craftcms/nitro/pkg/terminal"
	"github.com/craftcms/nitro/pkg/validate"
	"github.com/craftcms/nitro/pkg/webroot"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// CreateDatabase is used to interactively walk a user through creating a new database. It will return true if the user created a database along
// with the hostname, database, port, and driver for the database container.
func CreateDatabase(ctx context.Context, docker client.CommonAPIClient, output terminal.Outputer) (bool, string, string, string, string, error) {
	confirm, err := output.Confirm("Add a database for the site?", false, "?")
	if err != nil {
		return false, "", "", "", "", err
	}

	if !confirm {
		return false, "", "", "", "", nil
	}

	// add filters to show only the environment and database containers
	filter := filters.NewArgs()
	filter.Add("label", labels.Nitro)
	filter.Add("label", labels.Type+"=database")

	// get a list of all the databases
	containers, err := docker.ContainerList(ctx, types.ContainerListOptions{Filters: filter})
	if err != nil {
		return false, "", "", "", "", err
	}

	// sort containers by the name
	sort.SliceStable(containers, func(i, j int) bool {
		return containers[i].Names[0] < containers[j].Names[0]
	})

	// get all of the containers as a list
	var engineOpts []string
	for _, c := range containers {
		engineOpts = append(engineOpts, strings.TrimLeft(c.Names[0], "/"))
	}

	// prompt the user for the engine to add the database
	var containerID, databaseEngine string
	selected, err := output.Select(os.Stdin, "Select the database engine: ", engineOpts)
	if err != nil {
		return false, "", "", "", "", err
	}

	// set the container id and db engine
	containerID = containers[selected].ID
	databaseEngine = containers[selected].Labels[labels.DatabaseCompatibility]
	if containerID == "" {
		return false, "", "", "", "", fmt.Errorf("unable to get the container")
	}

	// ask the user for the database to create
	db, err := output.Ask("Enter the new database name", "", ":", nil)
	if err != nil {
		return false, "", "", "", "", err
	}

	output.Pending("creating database", db)

	// set the commands based on the engine type
	var cmds, privileges []string
	switch databaseEngine {
	case "mysql":
		cmds = []string{"mysql", "-uroot", "-pnitro", fmt.Sprintf(`-e CREATE DATABASE IF NOT EXISTS %s;`, db)}
		privileges = []string{"mysql", "-uroot", "-pnitro", fmt.Sprintf(`-e GRANT ALL PRIVILEGES ON * TO '%s'@'%s';`, "nitro", "%")}
	default:
		cmds = []string{"psql", "--username=nitro", "--host=127.0.0.1", fmt.Sprintf(`-c CREATE DATABASE %s;`, db)}
	}

	// create the exec
	e, err := docker.ContainerExecCreate(ctx, containerID, types.ExecConfig{
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Cmd:          cmds,
	})
	if err != nil {
		return false, "", "", "", "", err
	}

	// attach to the container
	resp, err := docker.ContainerExecAttach(ctx, e.ID, types.ExecStartCheck{
		Tty: false,
	})
	if err != nil {
		return false, "", "", "", "", err
	}
	defer resp.Close()

	// start the exec
	if err := docker.ContainerExecStart(ctx, e.ID, types.ExecStartCheck{}); err != nil {
		return false, "", "", "", "", fmt.Errorf("unable to start the container, %w", err)
	}

	// check if we should grant privileges
	if privileges != nil {
		// create the exec
		e, err := docker.ContainerExecCreate(ctx, containerID, types.ExecConfig{
			AttachStdout: true,
			AttachStderr: true,
			Tty:          false,
			Cmd:          privileges,
		})
		if err != nil {
			return false, "", "", "", "", err
		}

		// attach to the container
		resp, err := docker.ContainerExecAttach(ctx, e.ID, types.ExecStartCheck{
			Tty: false,
		})
		if err != nil {
			return false, "", "", "", "", err
		}
		defer resp.Close()

		// start the exec
		if err := docker.ContainerExecStart(ctx, e.ID, types.ExecStartCheck{}); err != nil {
			return false, "", "", "", "", fmt.Errorf("unable to start the container, %w", err)
		}

		// wait for the container exec to complete
		waiting := true
		for waiting {
			resp, err := docker.ContainerExecInspect(ctx, e.ID)
			if err != nil {
				return false, "", "", "", "", err
			}

			waiting = resp.Running
		}
	}

	output.Done()

	output.Info("Database added 💪")

	// get the container hostname
	hostname := strings.TrimLeft(containers[selected].Names[0], "/")

	// get the info from the container
	info, err := docker.ContainerInspect(ctx, containers[selected].ID)
	if err != nil {
		return false, "", "", "", "", err
	}

	var port string
	for p := range info.NetworkSettings.Ports {
		if port != "" {
			break
		}

		port = p.Port()
	}

	// set the driver for the database
	driver := "mysql"
	if containers[selected].Labels[labels.DatabaseCompatibility] == "postgres" {
		driver = "pgsql"
	}

	return true, hostname, db, port, driver, nil
}

// CreateSite takes the users home directory and the site path and walked the user
// through adding a site to the config.
func CreateSite(home, dir string, output terminal.Outputer) (*config.Site, error) {
	// create a new site
	site := config.Site{}

	// get the hostname from the directory
	// p := filepath.Join(dir)
	sp := strings.Split(filepath.Join(dir), string(os.PathSeparator))
	site.Hostname = sp[len(sp)-1]

	// append the test domain if there are no periods
	if !strings.Contains(site.Hostname, ".") {
		// set the default tld
		tld := "nitro"
		if os.Getenv("NITRO_DEFAULT_TLD") != "" {
			tld = os.Getenv("NITRO_DEFAULT_TLD")
		}

		site.Hostname = fmt.Sprintf("%s.%s", site.Hostname, tld)
	}

	// prompt for the hostname
	hostname, err := output.Ask("Enter the hostname", site.Hostname, ":", &validate.HostnameValidator{})
	if err != nil {
		return nil, err
	}

	// set the input as the hostname
	site.Hostname = hostname

	output.Success("setting hostname to", site.Hostname)

	// set the sites directory but make the path relative
	siteAbsPath, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	site.Path = strings.Replace(siteAbsPath, home, "~", 1)

	output.Success("adding site", site.Path)

	// get the web directory
	found, err := webroot.Find(dir)
	if err != nil {
		return nil, err
	}

	// if the root is still empty, we fall back to the default
	if found == "" {
		found = "web"
	}

	// set the webroot
	site.Webroot = found

	// prompt for the webroot
	root, err := output.Ask("Enter the webroot for the site", site.Webroot, ":", nil)
	if err != nil {
		return nil, err
	}

	site.Webroot = root

	output.Success("using webroot", site.Webroot)

	// prompt for the php version
	versions := phpversions.Versions
	selected, err := output.Select(os.Stdin, "Choose a PHP version: ", versions)
	if err != nil {
		return nil, err
	}

	// set the version of php
	site.Version = versions[selected]

	output.Success("setting PHP version", site.Version)

	// load the config
	cfg, err := config.Load(home)
	if err != nil {
		return nil, err
	}

	// add the site to the config
	if err := cfg.AddSite(site); err != nil {
		return nil, err
	}

	// save the config file
	if err := cfg.Save(); err != nil {
		return nil, err
	}

	return &site, nil
}