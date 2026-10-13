package internal

import (
	"context"

	"connectrpc.com/connect/v2"
	workerv1 "github.com/easydev-org/easyworker/gen/worker/v1"
	"github.com/easydev-org/easyworker/internal/computer"
	"github.com/easydev-org/easyworker/internal/jobsvc"
)

// ComputerService implements workerv1connect.ComputerServiceHandler. It is a
// thin, platform-agnostic RPC surface over the worker's computer.Driver: the
// client (the workspace extension) sends a logical operation and the worker
// picks the right tool for its own platform.
type ComputerService struct {
	drv computer.Driver
	// rec, when non-nil, registers each operation as a "computer" job so the UI
	// can show GUI automation separately from user jobs.
	rec *jobsvc.Manager
}

// NewComputerService builds the handler from the resolved driver.
func NewComputerService(drv computer.Driver, rec *jobsvc.Manager) *ComputerService {
	return &ComputerService{drv: drv, rec: rec}
}

// record logs one computer-use operation into the "computer" job namespace.
func (s *ComputerService) record(op string) {
	if s.rec != nil {
		s.rec.Record(jobsvc.NamespaceComputer, "computer "+op)
	}
}

func (s *ComputerService) Info(ctx context.Context, _ *workerv1.ComputerInfoRequest) (*workerv1.ComputerInfoResponse, error) {
	return &workerv1.ComputerInfoResponse{
		Platform: s.drv.Platform(),
		Display:  s.drv.Display(),
		Tools:    s.drv.Tools(),
	}, nil
}

func (s *ComputerService) Apps(ctx context.Context, _ *workerv1.ComputerAppsRequest) (*workerv1.ComputerAppsResponse, error) {
	s.record("apps")
	apps, err := s.drv.Apps(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	out := make([]*workerv1.ComputerApp, 0, len(apps))
	for _, a := range apps {
		out = append(out, &workerv1.ComputerApp{Pid: a.PID, Name: a.Name, Focused: a.Focused})
	}
	return &workerv1.ComputerAppsResponse{Apps: out}, nil
}

func (s *ComputerService) Snapshot(ctx context.Context, req *workerv1.ComputerSnapshotRequest) (*workerv1.ComputerSnapshotResponse, error) {
	s.record("snapshot")
	text, err := s.drv.Snapshot(ctx, req.GetApp(), int(req.GetDepth()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &workerv1.ComputerSnapshotResponse{Text: text}, nil
}

func (s *ComputerService) Find(ctx context.Context, req *workerv1.ComputerFindRequest) (*workerv1.ComputerFindResponse, error) {
	s.record("find " + req.GetSelector())
	text, err := s.drv.Find(ctx, req.GetSelector(), req.GetApp(), req.GetOutput())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &workerv1.ComputerFindResponse{Text: text}, nil
}

func (s *ComputerService) Action(ctx context.Context, req *workerv1.ComputerActionRequest) (*workerv1.ComputerActionResponse, error) {
	s.record("action " + req.GetAction() + " " + req.GetTarget())
	if err := s.drv.Action(ctx, req.GetAction(), req.GetTarget(), req.GetValue(), req.GetApp()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &workerv1.ComputerActionResponse{Ok: true}, nil
}

func (s *ComputerService) Click(ctx context.Context, req *workerv1.ComputerClickRequest) (*workerv1.ComputerClickResponse, error) {
	s.record("click")
	if err := s.drv.Click(ctx, int(req.GetX()), int(req.GetY()), req.GetButton(), int(req.GetCount())); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &workerv1.ComputerClickResponse{Ok: true}, nil
}

func (s *ComputerService) Type(ctx context.Context, req *workerv1.ComputerTypeRequest) (*workerv1.ComputerTypeResponse, error) {
	s.record("type")
	if err := s.drv.Type(ctx, req.GetText(), req.GetTarget()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &workerv1.ComputerTypeResponse{Ok: true}, nil
}

func (s *ComputerService) Key(ctx context.Context, req *workerv1.ComputerKeyRequest) (*workerv1.ComputerKeyResponse, error) {
	s.record("key " + req.GetKey())
	if err := s.drv.Key(ctx, req.GetKey(), req.GetHeld()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &workerv1.ComputerKeyResponse{Ok: true}, nil
}

func (s *ComputerService) Scroll(ctx context.Context, req *workerv1.ComputerScrollRequest) (*workerv1.ComputerScrollResponse, error) {
	s.record("scroll")
	if err := s.drv.Scroll(ctx, int(req.GetX()), int(req.GetY()), int(req.GetDx()), int(req.GetDy())); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &workerv1.ComputerScrollResponse{Ok: true}, nil
}

func (s *ComputerService) Drag(ctx context.Context, req *workerv1.ComputerDragRequest) (*workerv1.ComputerDragResponse, error) {
	s.record("drag")
	if err := s.drv.Drag(ctx, int(req.GetFromX()), int(req.GetFromY()), int(req.GetToX()), int(req.GetToY()), int(req.GetDurationMs())); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &workerv1.ComputerDragResponse{Ok: true}, nil
}

func (s *ComputerService) Screenshot(ctx context.Context, req *workerv1.ComputerScreenshotRequest) (*workerv1.ComputerScreenshotResponse, error) {
	s.record("screenshot")
	png, w, h, err := s.drv.Screenshot(ctx, req.GetRegion())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	if len(png) == 0 {
		return nil, connect.NewError(connect.CodeInternal, "empty screenshot")
	}
	return &workerv1.ComputerScreenshotResponse{Png: png, Width: int32(w), Height: int32(h)}, nil
}
