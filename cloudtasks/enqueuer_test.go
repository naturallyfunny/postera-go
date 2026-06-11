package cloudtasks_test

import (
	"context"
	"net"
	"strings"
	"testing"

	taskspb "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"

	"go.naturallyfunny.dev/postera"
	"go.naturallyfunny.dev/postera/cloudtasks"
)

// fakeCloudTasksServer is a minimal in-process Cloud Tasks gRPC server for tests.
type fakeCloudTasksServer struct {
	taskspb.UnimplementedCloudTasksServer
}

func (f *fakeCloudTasksServer) DeleteTask(_ context.Context, _ *taskspb.DeleteTaskRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (f *fakeCloudTasksServer) CreateTask(_ context.Context, req *taskspb.CreateTaskRequest) (*taskspb.Task, error) {
	return &taskspb.Task{Name: req.Task.Name}, nil
}

// newFakeEnqueuer starts a fake gRPC server and returns an Enqueuer wired to it.
func newFakeEnqueuer(t *testing.T, cfg cloudtasks.Config, opts ...cloudtasks.Option) *cloudtasks.Enqueuer {
	t.Helper()

	srv := grpc.NewServer()
	taskspb.RegisterCloudTasksServer(srv, &fakeCloudTasksServer{})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(lis) //nolint:errcheck
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	opts = append(opts,
		cloudtasks.WithGCPClientOption(option.WithGRPCConn(conn)),
		cloudtasks.WithGCPClientOption(option.WithoutAuthentication()),
	)

	enq, err := cloudtasks.NewEnqueuer(context.Background(), cfg, opts...)
	if err != nil {
		t.Fatalf("NewEnqueuer: %v", err)
	}
	t.Cleanup(func() { enq.Close() })
	return enq
}

func baseConfig() cloudtasks.Config {
	return cloudtasks.Config{ProjectID: "proj", LocationID: "us-central1", QueueID: "q"}
}

func TestNewEnqueuerRequiresQueueFields(t *testing.T) {
	_, err := cloudtasks.NewEnqueuer(context.Background(), cloudtasks.Config{TargetURL: "https://example.com/cb"})
	if err == nil {
		t.Fatal("expected error for missing queue fields, got nil")
	}
	if !strings.Contains(err.Error(), "ProjectID") {
		t.Fatalf("error should mention ProjectID, got: %v", err)
	}
}

func TestNewEnqueuerAcceptsEmptyTargetURL(t *testing.T) {
	// cancel-only consumers must be able to construct without a TargetURL
	_ = newFakeEnqueuer(t, baseConfig())
}

func TestEnqueueReturnsErrorWhenTargetURLEmpty(t *testing.T) {
	enq := newFakeEnqueuer(t, baseConfig()) // no TargetURL

	err := enq.Enqueue(context.Background(), postera.Posterum{ID: "x", Message: "m"})
	if err == nil {
		t.Fatal("expected error from Enqueue with empty TargetURL, got nil")
	}
	if !strings.Contains(err.Error(), "TargetURL") {
		t.Fatalf("expected TargetURL error, got: %v", err)
	}
}

func TestCancelWorksWithoutTargetURL(t *testing.T) {
	enq := newFakeEnqueuer(t, baseConfig()) // no TargetURL

	if err := enq.Cancel(context.Background(), "task-id-123"); err != nil {
		t.Fatalf("Cancel with no TargetURL: %v", err)
	}
}

func TestEnqueueSucceedsWithTargetURL(t *testing.T) {
	cfg := baseConfig()
	cfg.TargetURL = "https://example.com/callback"
	enq := newFakeEnqueuer(t, cfg)

	if err := enq.Enqueue(context.Background(), postera.Posterum{ID: "task-1", Message: "hello"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
}
