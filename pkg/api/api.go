package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/craftcms/nitro/pkg/caddy"
	"github.com/craftcms/nitro/pkg/database"
	"github.com/craftcms/nitro/pkg/portavail"
	"github.com/craftcms/nitro/protob"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var Version string

// NewService takes the address to the Caddy API and returns an API struct that
// implements the gRPC API used in the proxy container. The gRPC API is used to
// handle making changes to the Caddy Server via its local API. If no addr is
// provided, it will set the default addr to http://127.0.0.1:2019
func NewService(addr string) protob.NitroServer {
	return &Service{
		Addr:     addr,
		HTTP:     http.DefaultClient,
		Importer: database.NewImporter(),
	}
}

// Service implements the protob.NitroServer interface
type Service struct {
	Addr     string
	HTTP     *http.Client
	Importer database.Importer
}

// Ping returns a simple response "pong" from the gRPC API to verify connectivity.
func (svc *Service) Ping(ctx context.Context, request *protob.PingRequest) (*protob.PingResponse, error) {
	return &protob.PingResponse{Pong: "pong"}, nil
}

// Apply is used to take all of the sites from a Nitro config and apply those changes. The Sites
// in protob.ApplyRequest represents the hostname, aliases (in a comma delimited list), and the
// port for the service. The NGINX container type uses port 8080 and the PHP-FPM container type
// uses port 9000.
func (svc *Service) Apply(ctx context.Context, request *protob.ApplyRequest) (*protob.ApplyResponse, error) {
	// if there is no client, use the default
	if svc.HTTP == nil {
		svc.HTTP = http.DefaultClient
	}

	// set the addr if not provided
	if svc.Addr == "" {
		svc.Addr = "http://127.0.0.1:2019"
	}

	// convert each of the sites into a route
	routes := []caddy.ServerRoute{}
	for k, site := range request.GetSites() {
		// get all of the host names for the site
		hosts := []string{site.GetHostname()}
		if site.GetAliases() != "" {
			hosts = append(hosts, strings.Split(site.GetAliases(), ",")...)
		}

		// create the route for each of the sites
		routes = append(routes, caddy.ServerRoute{
			Handle: []caddy.RouteHandle{
				{
					Handler: "reverse_proxy",
					Upstreams: []caddy.Upstream{
						{
							Dial: fmt.Sprintf("%s:%d", k, site.GetPort()),
						},
					},
				},
			},
			Match: []caddy.Match{
				{
					Host: hosts,
				},
			},
			Terminal: true,
		})
	}

	update := caddy.UpdateRequest{}

	// add the routes to the first server
	update.HTTPS = caddy.Server{
		Listen: []string{":443"},
		Routes: routes,
	}

	// set the default welcome server
	update.HTTP = caddy.Server{
		Listen: []string{":80"},
		Routes: append(routes, caddy.ServerRoute{
			Handle: []caddy.RouteHandle{
				{
					Handler: "vars",
					Root:    "/var/www/html",
				},
				{
					Handler: "file_server",
					Root:    "/var/www/html",
					Hide:    []string{"/etc/caddy/Caddyfile"},
				},
			},
			Terminal: true,
		}),
	}

	content, err := json.Marshal(&update)
	if err != nil {
		return nil, err
	}

	// send the update
	res, err := svc.HTTP.Post(svc.Addr+"/config/apps/http/servers", "application/json", bytes.NewReader(content))
	if err != nil {
		return &protob.ApplyResponse{
			Message: fmt.Sprintf("Error updating Caddy API, err: %s", err.Error()),
			Error:   true,
		}, err
	}

	// check the status code
	if res.StatusCode != http.StatusOK {
		return &protob.ApplyResponse{
			Message: fmt.Sprintf("Received %d response from Caddy API", res.StatusCode),
			Error:   true,
		}, nil
	}

	// set the message and error to false
	return &protob.ApplyResponse{
		Message: fmt.Sprintf("Successfully applied changes, sites: %d", len(request.GetSites())),
		Error:   false,
	}, nil
}

// Version is used to check the container image version with the CLI version
func (svc *Service) Version(ctx context.Context, request *protob.VersionRequest) (*protob.VersionResponse, error) {
	return &protob.VersionResponse{Version: Version}, nil
}

// AddDatabase handle creating a new database for a hostname
func (svc *Service) AddDatabase(ctx context.Context, req *protob.AddDatabaseRequest) (*protob.AddDatabaseResponse, error) {
	// get the database info from the request
	hostname := req.GetDatabase().GetHostname()
	port := req.GetDatabase().GetPort()
	engine := req.GetDatabase().GetEngine()
	version := req.GetDatabase().GetVersion()
	db := req.GetDatabase().GetDatabase()

	// TODO(jasonmccallister) validate the request

	// verify we can connect to the database hostname - no error means its reachable
	if err := portavail.Check(hostname, port); err == nil {
		return nil, status.Errorf(codes.Internal, "it does not appear the database is available on host %s using port %s: %v", hostname, port, err)
	}

	// find the tool based on the engine
	tool, err := database.DefaultImportToolFinder(engine, version)
	if err != nil {
		return nil, status.Error(codes.Internal, "error finding the database tool")
	}

	// run the commands to add the database
	var addCommand, privilegesCommand []string
	switch engine {
	case "mysql":
		addCommand = []string{"--user=nitro", fmt.Sprintf("--host=%s", hostname), "-pnitro", fmt.Sprintf(`-e CREATE DATABASE IF NOT EXISTS %s;`, db)}
		privilegesCommand = []string{"--user=nitro", fmt.Sprintf("--host=%s", hostname), "-pnitro", fmt.Sprintf(`-e CREATE DATABASE IF NOT EXISTS %s;`, db)}
	default:
		addCommand = []string{fmt.Sprintf("--host=%s", hostname), "--port=" + port, "--username=nitro", fmt.Sprintf(`-c CREATE DATABASE %s;`, db)}
	}

	// add the database
	if err := svc.exec(tool, addCommand); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("error creating database: %s", err.Error()))
	}

	// set privileges if required
	if privilegesCommand != nil {
		if err := svc.exec(tool, addCommand); err != nil {
			return nil, status.Error(codes.Internal, fmt.Sprintf("error setting privileges on database: %s", err.Error()))
		}
	}

	return &protob.AddDatabaseResponse{Message: fmt.Sprintf("Database %q added to %q successfully", db, hostname)}, nil
}

func (svc *Service) exec(tool string, commands []string) error {
	c := exec.Command(tool, commands...)

	c.Stderr = os.Stderr
	c.Stdout = os.Stdout

	if err := c.Start(); err != nil {
		return fmt.Errorf("unable to start the command: %w", err)
	}

	if err := c.Wait(); err != nil {
		if exiterr, ok := err.(*exec.ExitError); ok {
			// The program has exited with an exit code != 0
			if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
				return fmt.Errorf("Exit Status: %d", status.ExitStatus())
			}
		} else {
			return err
		}
	}

	return nil
}

// ImportDatabase is used to handle streaming requests from the client and import a
// database from a backup into the remote database container.
func (svc *Service) ImportDatabase(stream protob.Nitro_ImportDatabaseServer) error {
	// verify the importer is declared
	if svc.Importer == nil {
		svc.Importer = database.NewImporter()
	}

	// create the options for the import
	opts := database.ImportOptions{}

	// create a temp file used to import the database content
	tempFile, err := ioutil.TempFile(os.TempDir(), "nitro-db-import")
	if err != nil {
		return status.Errorf(codes.Internal, "Unable creating a temp file for the upload")
	}

	// defer the file close and deletion
	defer tempFile.Close()
	defer os.Remove(tempFile.Name())

	// set the temporary file
	opts.File = tempFile.Name()

	req, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.Internal, "unable to receive from stream: %s", err.Error())
	}

	// get the database engine
	if opts.Engine == "" {
		opts.Engine = req.GetDatabase().GetEngine()
	}

	// get the database version
	if opts.Version == "" {
		opts.Version = req.GetDatabase().GetVersion()
	}

	// get the database port
	if opts.Port == "" {
		opts.Port = req.GetDatabase().GetPort()
	}

	// get the database hostname
	if opts.Hostname == "" {
		opts.Hostname = req.GetDatabase().GetHostname()
	}

	// get the database name
	if opts.DatabaseName == "" {
		opts.DatabaseName = req.GetDatabase().GetDatabase()
	}

	// check if the file is compressed
	if (!opts.Compressed) && (req.GetDatabase().GetCompressed()) {
		opts.Compressed = req.GetDatabase().GetCompressed()
	}

	// handle the streaming request
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return status.Errorf(codes.Internal, "unable to create the stream: %s", err.Error())
		}

		// write the streamed content into the temp file
		_, err = tempFile.Write(req.GetData())
		if err != nil && !errors.Is(err, io.EOF) {
			return status.Errorf(codes.Internal, "unable to write content to the temp file")
		}
	}

	// verify we can connect to the database hostname - no error means its reachable
	if err := portavail.Check(opts.Hostname, opts.Port); err == nil {
		return status.Errorf(codes.Internal, "it does not appear the database is available on host %s using port %s: %v", opts.Hostname, opts.Port, err)
	}

	// import the database
	if err := database.NewImporter().Import(&opts, database.DefaultImportToolFinder); err != nil {
		return status.Errorf(codes.Internal, "error importing the database %v", err)
	}

	// send and close the stream
	return stream.SendAndClose(
		&protob.ImportDatabaseResponse{
			Message: fmt.Sprintf("Imported database %q", opts.DatabaseName),
		},
	)
}

// RemoveDatabase handles removing a specific database from a database container
func (svc *Service) RemoveDatabase(ctx context.Context, req *protob.RemoveDatabaseRequest) (*protob.RemoveDatabaseResponse, error) {
	// get the database info from the request
	hostname := req.GetDatabase().GetHostname()
	port := req.GetDatabase().GetPort()
	engine := req.GetDatabase().GetEngine()
	version := req.GetDatabase().GetVersion()
	db := req.GetDatabase().GetDatabase()

	// TODO(jasonmccallister) validate the request

	// verify we can connect to the database hostname - no error means its reachable
	if err := portavail.Check(hostname, port); err == nil {
		return nil, status.Errorf(codes.Internal, "it does not appear the database is available on host %s using port %s: %v", hostname, port, err)
	}

	// find the tool based on the engine
	tool, err := database.DefaultImportToolFinder(engine, version)
	if err != nil {
		return nil, status.Error(codes.Internal, "error finding the database tool")
	}

	// run the commands to remove the database
	var removeCommand []string
	switch engine {
	case "mysql":
		removeCommand = []string{"--user=nitro", fmt.Sprintf("--host=%s", hostname), "-pnitro", fmt.Sprintf(`-e DROP DATABASE IF EXISTS %s;`, db)}
	default:
		removeCommand = []string{fmt.Sprintf("--host=%s", hostname), "--port=" + port, "--username=nitro", fmt.Sprintf(`-c DROP DATABASE IF EXISTS %s;`, db)}
	}

	// remove the database
	if err := svc.exec(tool, removeCommand); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("error removing database: %s", err.Error()))
	}

	return &protob.RemoveDatabaseResponse{
		Message: fmt.Sprintf("Removed the database %q from %q successfully", db, hostname),
	}, nil
}
