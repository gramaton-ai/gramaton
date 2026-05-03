package api

import "context"

// tenantContextKey is the unexported type for the context key under
// which a caller's tenant identifier is stashed. Using an unexported
// named type prevents external packages from colliding with us.
type tenantContextKey struct{}

// WithTenant returns a derived context carrying the supplied tenant
// identifier. Empty string is treated identically to "no tenant"
// (single-tenant deployment); pass it explicitly only when you have
// a meaningful identity.
//
// The api/ layer reads this back via tenantFromContext when creating
// or filtering jobs. Until real caller identity wires in (HTTP
// middleware on a remote-callable mode, MCP session token, etc.),
// every request runs with tenant="" and the persisted Job records
// reflect that. The data layer is ready for the multi-tenant
// switch-over without a migration.
func WithTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantContextKey{}, tenant)
}

// tenantFromContext returns the tenant identifier the caller stamped
// onto ctx via WithTenant, or "" if none. Single-tenant single-process
// deployments never set the value, so this returns "" everywhere
// today.
//
// All Job-creating, Job-reading, and Job-listing api methods MUST
// route their tenant value through this helper. JobStore queries
// always pin TenantID to this value (empty matches empty); single-job
// projections check Job.TenantID == tenant before returning, else
// surface ErrNotFound to avoid leaking existence across tenants.
func tenantFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(tenantContextKey{}).(string); ok {
		return v
	}
	return ""
}

// tenantOwnsJob is the inverse of "leak existence". When a caller's
// tenant doesn't match the Job's TenantID, every single-job api
// surface (Status, Cancel, Result) returns ErrNotFound rather than
// ErrForbidden -- forbidden would still confirm the JobID exists.
// In single-tenant deployments where both sides are "" this is
// always true; only when real identity propagates does it filter.
func tenantOwnsJob(callerTenant, jobTenant string) bool {
	return callerTenant == jobTenant
}
