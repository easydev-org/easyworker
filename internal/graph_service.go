package internal

// JobGraph / JobFlow expose a job's easysh dataflow graph over the Connect API,
// mirroring easysh's own /api/jobs/{id}/graph and /flows/{fid}. The panel uses
// them to show which command produced which stream, how they were piped, and
// what each intermediate result contained.

import (
	"context"

	"connectrpc.com/connect/v2"
	workerv1 "github.com/easydev-org/easyworker/gen/worker/v1"
)

// JobGraph returns a job's flows and the edges between them.
func (s *WorkerService) JobGraph(ctx context.Context, req *workerv1.JobGraphRequest) (*workerv1.JobGraphResponse, error) {
	rows, err := s.jobs.Store().Flow.Flows(req.JobId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	edges, err := s.jobs.Store().Flow.Edges(req.JobId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	resp := &workerv1.JobGraphResponse{}
	for _, r := range rows {
		resp.Flows = append(resp.Flows, &workerv1.Flow{
			Fid:      r.FID,
			Kind:     r.Kind,
			Fd:       r.Fd,
			Owner:    r.Owner,
			Producer: r.Producer,
			Consumer: r.Consumer,
			Path:     r.Path,
			Bytes:    r.Bytes,
			Lines:    r.Lines,
			Line:     int32(r.Line),
			Col:      int32(r.Col),
			EndLine:  int32(r.EndLine),
			EndCol:   int32(r.EndCol),
			Iter:     r.Iter,
		})
	}
	for _, e := range edges {
		resp.Edges = append(resp.Edges, &workerv1.FlowEdge{FromFid: e[0], ToFid: e[1]})
	}
	return resp, nil
}

// JobFlow returns one flow's intermediate bytes (the content a pipe carried).
func (s *WorkerService) JobFlow(ctx context.Context, req *workerv1.JobFlowRequest) (*workerv1.JobFlowResponse, error) {
	b, err := s.jobs.Store().Flow.Content(req.JobId, req.Fid)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &workerv1.JobFlowResponse{Data: b}, nil
}
