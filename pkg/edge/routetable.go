package edge

type RouteTableFormat string

const (
	RouteTableNext RouteTableFormat = "next"
	RouteTableOcel RouteTableFormat = "ocel"
)

const (
	NextRouteTableFile = "next-route-table.json"
	OcelRoutesFile     = "routes.json"
)
