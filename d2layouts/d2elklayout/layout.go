// d2elklayout is a wrapper around the Javascript port of ELK.
//
// Coordinates are relative to parents.
// See https://www.eclipse.org/elk/documentation/tooldevelopers/graphdatastructure/coordinatesystem.html
package d2elklayout

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/dop251/goja"

	"oss.terrastruct.com/util-go/xdefer"

	"oss.terrastruct.com/util-go/go2"

	"oss.terrastruct.com/d2/d2graph"
	"oss.terrastruct.com/d2/d2target"
	"oss.terrastruct.com/d2/lib/geo"
	"oss.terrastruct.com/d2/lib/label"
	"oss.terrastruct.com/d2/lib/shape"
)

//go:embed elk.js
var elkJS string

//go:embed setup.js
var setupJS string

type ELKNode struct {
	ID            string      `json:"id"`
	X             float64     `json:"x"`
	Y             float64     `json:"y"`
	Width         float64     `json:"width"`
	Height        float64     `json:"height"`
	Children      []*ELKNode  `json:"children,omitempty"`
	Labels        []*ELKLabel `json:"labels,omitempty"`
	LayoutOptions *elkOpts    `json:"layoutOptions,omitempty"`
}

type ELKLabel struct {
	Text          string   `json:"text"`
	X             float64  `json:"x"`
	Y             float64  `json:"y"`
	Width         float64  `json:"width"`
	Height        float64  `json:"height"`
	LayoutOptions *elkOpts `json:"layoutOptions,omitempty"`
}

type ELKPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type ELKEdgeSection struct {
	Start      ELKPoint   `json:"startPoint"`
	End        ELKPoint   `json:"endPoint"`
	BendPoints []ELKPoint `json:"bendPoints,omitempty"`
}

type ELKEdge struct {
	ID        string           `json:"id"`
	Sources   []string         `json:"sources"`
	Targets   []string         `json:"targets"`
	Sections  []ELKEdgeSection `json:"sections,omitempty"`
	Labels    []*ELKLabel      `json:"labels,omitempty"`
	Container string           `json:"container"`
}

type ELKGraph struct {
	ID            string     `json:"id"`
	LayoutOptions *elkOpts   `json:"layoutOptions"`
	Children      []*ELKNode `json:"children,omitempty"`
	Edges         []*ELKEdge `json:"edges,omitempty"`
}

type ConfigurableOpts struct {
	Algorithm       string `json:"elk.algorithm,omitempty"`
	NodeSpacing     int    `json:"spacing.nodeNodeBetweenLayers,omitempty"`
	Padding         string `json:"elk.padding,omitempty"`
	EdgeNodeSpacing int    `json:"spacing.edgeNodeBetweenLayers,omitempty"`
	SelfLoopSpacing int    `json:"elk.spacing.nodeSelfLoop"`
}

var DefaultOpts = ConfigurableOpts{
	Algorithm:       "layered",
	NodeSpacing:     70.0,
	Padding:         "[top=50,left=50,bottom=50,right=50]",
	EdgeNodeSpacing: 40.0,
	SelfLoopSpacing: 50.0,
}

var port_spacing = 40.
var edge_node_spacing = 40

type elkOpts struct {
	EdgeNode                     int    `json:"elk.spacing.edgeNode,omitempty"`
	FixedAlignment               string `json:"elk.layered.nodePlacement.bk.fixedAlignment,omitempty"`
	Thoroughness                 int    `json:"elk.layered.thoroughness,omitempty"`
	EdgeEdgeBetweenLayersSpacing int    `json:"elk.layered.spacing.edgeEdgeBetweenLayers,omitempty"`
	Direction                    string `json:"elk.direction"`
	HierarchyHandling            string `json:"elk.hierarchyHandling,omitempty"`
	InlineEdgeLabels             bool   `json:"elk.edgeLabels.inline,omitempty"`
	ForceNodeModelOrder          bool   `json:"elk.layered.crossingMinimization.forceNodeModelOrder,omitempty"`
	ConsiderModelOrder           string `json:"elk.layered.considerModelOrder.strategy,omitempty"`

	NodeSizeConstraints string `json:"elk.nodeSize.constraints,omitempty"`
	NodeSizeMinimum     string `json:"elk.nodeSize.minimum,omitempty"`

	ConfigurableOpts
}

func DefaultLayout(ctx context.Context, g *d2graph.Graph) (err error) {
	return Layout(ctx, g, nil)
}

func Layout(ctx context.Context, g *d2graph.Graph, opts *ConfigurableOpts) (err error) {
	if opts == nil {
		opts = &DefaultOpts
	}
	defer xdefer.Errorf(&err, "failed to ELK layout")

	vm := goja.New()

	console := vm.NewObject()
	if err := vm.Set("console", console); err != nil {
		return err
	}

	if _, err := vm.RunString(elkJS); err != nil {
		return err
	}
	if _, err := vm.RunString(setupJS); err != nil {
		return err
	}

	elkGraph := &ELKGraph{
		ID: "root",
		LayoutOptions: &elkOpts{
			Thoroughness:                 8,
			EdgeEdgeBetweenLayersSpacing: 50,
			EdgeNode:                     edge_node_spacing,
			HierarchyHandling:            "INCLUDE_CHILDREN",
			FixedAlignment:               "BALANCED",
			ConsiderModelOrder:           "NODES_AND_EDGES",
			ConfigurableOpts: ConfigurableOpts{
				Algorithm:       opts.Algorithm,
				NodeSpacing:     opts.NodeSpacing,
				EdgeNodeSpacing: opts.EdgeNodeSpacing,
				SelfLoopSpacing: opts.SelfLoopSpacing,
			},
		},
	}
	switch g.Root.Attributes.Direction.Value {
	case "down":
		elkGraph.LayoutOptions.Direction = "DOWN"
	case "up":
		elkGraph.LayoutOptions.Direction = "UP"
	case "right":
		elkGraph.LayoutOptions.Direction = "RIGHT"
	case "left":
		elkGraph.LayoutOptions.Direction = "LEFT"
	default:
		elkGraph.LayoutOptions.Direction = "DOWN"
	}

	elkNodes := make(map[*d2graph.Object]*ELKNode)
	elkEdges := make(map[*d2graph.Edge]*ELKEdge)

	// BFS
	var walk func(*d2graph.Object, *d2graph.Object, func(*d2graph.Object, *d2graph.Object))
	walk = func(obj, parent *d2graph.Object, fn func(*d2graph.Object, *d2graph.Object)) {
		if obj.Parent != nil {
			fn(obj, parent)
		}
		for _, ch := range obj.ChildrenArray {
			walk(ch, obj, fn)
		}
	}

	walk(g.Root, nil, func(obj, parent *d2graph.Object) {
		incoming := 0.
		outgoing := 0.
		for _, e := range g.Edges {
			if e.Src == obj {
				outgoing++
			}
			if e.Dst == obj {
				incoming++
			}
		}
		if incoming >= 2 || outgoing >= 2 {
			switch g.Root.Attributes.Direction.Value {
			case "right", "left":
				obj.Height = math.Max(obj.Height, math.Max(incoming, outgoing)*port_spacing)
			default:
				obj.Width = math.Max(obj.Width, math.Max(incoming, outgoing)*port_spacing)
			}
		}

		height := obj.Height
		width := obj.Width
		if obj.LabelWidth != nil && obj.LabelHeight != nil {
			if obj.HasOutsideBottomLabel() || obj.Attributes.Icon != nil {
				height += float64(*obj.LabelHeight) + label.PADDING
			}
			width = go2.Max(width, float64(*obj.LabelWidth))
		}

		n := &ELKNode{
			ID:     obj.AbsID(),
			Width:  width,
			Height: height,
		}

		if len(obj.ChildrenArray) > 0 {
			n.LayoutOptions = &elkOpts{
				ForceNodeModelOrder:          true,
				Thoroughness:                 8,
				EdgeEdgeBetweenLayersSpacing: 50,
				HierarchyHandling:            "INCLUDE_CHILDREN",
				FixedAlignment:               "BALANCED",
				EdgeNode:                     edge_node_spacing,
				ConsiderModelOrder:           "NODES_AND_EDGES",
				// Why is it (height, width)? I have no clue, but it works.
				NodeSizeMinimum: fmt.Sprintf("(%d, %d)", int(math.Ceil(height)), int(math.Ceil(width))),
				ConfigurableOpts: ConfigurableOpts{
					NodeSpacing:     opts.NodeSpacing,
					EdgeNodeSpacing: opts.EdgeNodeSpacing,
					SelfLoopSpacing: opts.SelfLoopSpacing,
					Padding:         opts.Padding,
				},
			}
			// Only set if specified.
			// There's a bug where if it's the node label dimensions that set the NodeSizeMinimum,
			// then suddenly it's reversed back to (width, height). I must be missing something
			if obj.Attributes.Width != nil || obj.Attributes.Height != nil {
				n.LayoutOptions.NodeSizeConstraints = "MINIMUM_SIZE"
			}

			if n.LayoutOptions.Padding == DefaultOpts.Padding {
				// Default
				paddingTop := 50
				if obj.LabelHeight != nil {
					paddingTop = go2.Max(paddingTop, *obj.LabelHeight+label.PADDING)
				}
				if obj.Attributes.Icon != nil && obj.Attributes.Shape.Value != d2target.ShapeImage {
					contentBox := geo.NewBox(geo.NewPoint(0, 0), float64(n.Width), float64(n.Height))
					shapeType := d2target.DSL_SHAPE_TO_SHAPE_TYPE[obj.Attributes.Shape.Value]
					s := shape.NewShape(shapeType, contentBox)
					iconSize := d2target.GetIconSize(s.GetInnerBox(), string(label.InsideTopLeft))
					paddingTop = go2.Max(paddingTop, iconSize+label.PADDING*2)
				}
				n.LayoutOptions.Padding = fmt.Sprintf("[top=%d,left=50,bottom=50,right=50]",
					paddingTop,
				)
			}
		} else {
			n.LayoutOptions = &elkOpts{
				// Margins: "[top=100,left=100,bottom=100,right=100]",
			}
		}

		if obj.LabelWidth != nil && obj.LabelHeight != nil {
			n.Labels = append(n.Labels, &ELKLabel{
				Text:   obj.Attributes.Label.Value,
				Width:  float64(*obj.LabelWidth),
				Height: float64(*obj.LabelHeight),
			})
		}

		if parent == g.Root {
			elkGraph.Children = append(elkGraph.Children, n)
		} else {
			elkNodes[parent].Children = append(elkNodes[parent].Children, n)
		}
		elkNodes[obj] = n
	})

	for _, edge := range g.Edges {
		e := &ELKEdge{
			ID:      edge.AbsID(),
			Sources: []string{edge.Src.AbsID()},
			Targets: []string{edge.Dst.AbsID()},
		}
		if edge.Attributes.Label.Value != "" {
			e.Labels = append(e.Labels, &ELKLabel{
				Text:   edge.Attributes.Label.Value,
				Width:  float64(edge.LabelDimensions.Width),
				Height: float64(edge.LabelDimensions.Height),
				LayoutOptions: &elkOpts{
					InlineEdgeLabels: true,
				},
			})
		}
		elkGraph.Edges = append(elkGraph.Edges, e)
		elkEdges[edge] = e
	}

	raw, err := json.Marshal(elkGraph)
	if err != nil {
		return err
	}

	loadScript := fmt.Sprintf(`var graph = %s`, raw)

	if _, err := vm.RunString(loadScript); err != nil {
		return err
	}

	val, err := vm.RunString(`elk.layout(graph)
.then(s => s)
.catch(err => err.message)
`)

	if err != nil {
		return err
	}

	p := val.Export()
	if err != nil {
		return err
	}

	promise := p.(*goja.Promise)

	for promise.State() == goja.PromiseStatePending {
		if err := ctx.Err(); err != nil {
			return err
		}
		continue
	}

	if promise.State() == goja.PromiseStateRejected {
		return errors.New("ELK: something went wrong")
	}

	result := promise.Result().Export()

	var jsonOut map[string]interface{}
	switch out := result.(type) {
	case string:
		return fmt.Errorf("ELK layout error: %s", out)
	case map[string]interface{}:
		jsonOut = out
	default:
		return fmt.Errorf("ELK unexpected return: %v", out)
	}

	jsonBytes, err := json.Marshal(jsonOut)
	if err != nil {
		return err
	}

	err = json.Unmarshal(jsonBytes, &elkGraph)
	if err != nil {
		return err
	}

	byID := make(map[string]*d2graph.Object)
	walk(g.Root, nil, func(obj, parent *d2graph.Object) {
		n := elkNodes[obj]

		parentX := 0.0
		parentY := 0.0
		if parent != nil && parent != g.Root {
			parentX = parent.TopLeft.X
			parentY = parent.TopLeft.Y
		}
		obj.TopLeft = geo.NewPoint(parentX+n.X, parentY+n.Y)
		obj.Width = n.Width
		obj.Height = n.Height

		if obj.LabelWidth != nil && obj.LabelHeight != nil {
			if len(obj.ChildrenArray) > 0 {
				obj.LabelPosition = go2.Pointer(string(label.InsideTopCenter))
			} else if obj.HasOutsideBottomLabel() {
				obj.LabelPosition = go2.Pointer(string(label.OutsideBottomCenter))
				obj.Height -= float64(*obj.LabelHeight) + label.PADDING
			} else if obj.Attributes.Icon != nil {
				obj.LabelPosition = go2.Pointer(string(label.InsideTopCenter))
			} else {
				obj.LabelPosition = go2.Pointer(string(label.InsideMiddleCenter))
			}
		}
		if obj.Attributes.Icon != nil {
			if len(obj.ChildrenArray) > 0 {
				obj.IconPosition = go2.Pointer(string(label.InsideTopLeft))
				obj.LabelPosition = go2.Pointer(string(label.InsideTopRight))
			} else {
				obj.IconPosition = go2.Pointer(string(label.InsideMiddleCenter))
			}
		}

		byID[obj.AbsID()] = obj
	})

	for _, edge := range g.Edges {
		e := elkEdges[edge]

		parentX := 0.0
		parentY := 0.0
		if e.Container != "root" {
			parentX = byID[e.Container].TopLeft.X
			parentY = byID[e.Container].TopLeft.Y
		}

		var points []*geo.Point
		for _, s := range e.Sections {
			points = append(points, &geo.Point{
				X: parentX + s.Start.X,
				Y: parentY + s.Start.Y,
			})
			for _, bp := range s.BendPoints {
				points = append(points, &geo.Point{
					X: parentX + bp.X,
					Y: parentY + bp.Y,
				})
			}
			points = append(points, &geo.Point{
				X: parentX + s.End.X,
				Y: parentY + s.End.Y,
			})
		}

		startIndex, endIndex := 0, len(points)-1
		srcShape := shape.NewShape(d2target.DSL_SHAPE_TO_SHAPE_TYPE[strings.ToLower(edge.Src.Attributes.Shape.Value)], edge.Src.Box)
		dstShape := shape.NewShape(d2target.DSL_SHAPE_TO_SHAPE_TYPE[strings.ToLower(edge.Dst.Attributes.Shape.Value)], edge.Dst.Box)

		// trace the edge to the specific shape's border
		points[startIndex] = shape.TraceToShapeBorder(srcShape, points[startIndex], points[startIndex+1])
		points[endIndex] = shape.TraceToShapeBorder(dstShape, points[endIndex], points[endIndex-1])

		if edge.Attributes.Label.Value != "" {
			edge.LabelPosition = go2.Pointer(string(label.InsideMiddleCenter))
		}

		edge.Route = points
	}

	deleteBends(g)

	return nil
}

// deleteBends is a shim for ELK to delete unnecessary bends
// see https://github.com/terrastruct/d2/issues/1030
func deleteBends(g *d2graph.Graph) {
	// Get rid of S-shapes at the source and the target
	// TODO there might be value in repeating this. removal of an S shape introducing another S shape that can still be removed
	for _, isSource := range []bool{true, false} {
		for ei, e := range g.Edges {
			if len(e.Route) < 4 {
				continue
			}
			if e.Src == e.Dst {
				continue
			}
			var endpoint *d2graph.Object
			var start *geo.Point
			var corner *geo.Point
			var end *geo.Point

			if isSource {
				start = e.Route[0]
				corner = e.Route[1]
				end = e.Route[2]
				endpoint = e.Src
			} else {
				start = e.Route[len(e.Route)-1]
				corner = e.Route[len(e.Route)-2]
				end = e.Route[len(e.Route)-3]
				endpoint = e.Dst
			}

			isHorizontal := start.Y == corner.Y

			// Make sure it's still attached
			if isHorizontal {
				if end.Y <= endpoint.TopLeft.Y+10 {
					continue
				}
				if end.Y >= endpoint.TopLeft.Y+endpoint.Height-10 {
					continue
				}
			} else {
				if end.X <= endpoint.TopLeft.X+10 {
					continue
				}
				if end.X >= endpoint.TopLeft.X+endpoint.Width-10 {
					continue
				}
			}

			var newStart *geo.Point
			if isHorizontal {
				newStart = geo.NewPoint(start.X, end.Y)
			} else {
				newStart = geo.NewPoint(end.X, start.Y)
			}

			endpointShape := shape.NewShape(d2target.DSL_SHAPE_TO_SHAPE_TYPE[strings.ToLower(endpoint.Attributes.Shape.Value)], endpoint.Box)
			newStart = shape.TraceToShapeBorder(endpointShape, newStart, end)

			// Check that the new segment doesn't collide with anything new

			oldSegment := geo.NewSegment(start, corner)
			newSegment := geo.NewSegment(newStart, end)

			oldIntersects := countObjectIntersects(g, *oldSegment)
			newIntersects := countObjectIntersects(g, *newSegment)

			if newIntersects > oldIntersects {
				continue
			}

			oldCrossingsCount, oldOverlapsCount, oldCloseOverlapsCount := countEdgeIntersects(g, g.Edges[ei], *oldSegment)
			newCrossingsCount, newOverlapsCount, newCloseOverlapsCount := countEdgeIntersects(g, g.Edges[ei], *newSegment)

			if newCrossingsCount > oldCrossingsCount {
				continue
			}
			if newOverlapsCount > oldOverlapsCount {
				continue
			}

			if newCloseOverlapsCount > oldCloseOverlapsCount {
				continue
			}

			// commit
			if isSource {
				g.Edges[ei].Route = append(
					[]*geo.Point{newStart},
					e.Route[3:]...,
				)
			} else {
				g.Edges[ei].Route = append(
					e.Route[:len(e.Route)-3],
					newStart,
				)
			}
		}
	}
	// Get rid of ladders
	// ELK likes to do these for some reason
	// .   ┌─
	// . ┌─┘
	// . │
	// We want to transform these into L-shapes
	for ei, e := range g.Edges {
		if len(e.Route) < 6 {
			continue
		}
		if e.Src == e.Dst {
			continue
		}

		for i := 1; i < len(e.Route)-3; i++ {
			before := e.Route[i-1]
			start := e.Route[i]
			corner := e.Route[i+1]
			end := e.Route[i+2]
			after := e.Route[i+3]

			// S-shape on sources only concerned one segment, since the other was just along the bound of endpoint
			// These concern two segments

			var newCorner *geo.Point
			if start.X == corner.X {
				newCorner = geo.NewPoint(end.X, start.Y)
				// not ladder
				if (end.Y > start.Y) != (before.Y > start.Y) {
					continue
				}
				if (end.Y > start.Y) != (end.Y > start.Y) {
					continue
				}
			} else {
				newCorner = geo.NewPoint(start.X, end.Y)
				if (end.X > start.X) != (before.X > start.X) {
					continue
				}
				if (end.X > start.X) != (after.X > start.X) {
					continue
				}
			}

			oldS1 := geo.NewSegment(start, corner)
			oldS2 := geo.NewSegment(corner, end)

			newS1 := geo.NewSegment(start, newCorner)
			newS2 := geo.NewSegment(newCorner, end)

			// Check that the new segments doesn't collide with anything new
			oldIntersects := countObjectIntersects(g, *oldS1) + countObjectIntersects(g, *oldS2)
			newIntersects := countObjectIntersects(g, *newS1) + countObjectIntersects(g, *newS2)

			if newIntersects > oldIntersects {
				continue
			}

			oldCrossingsCount1, oldOverlapsCount1, oldCloseOverlapsCount1 := countEdgeIntersects(g, g.Edges[ei], *oldS1)
			oldCrossingsCount2, oldOverlapsCount2, oldCloseOverlapsCount2 := countEdgeIntersects(g, g.Edges[ei], *oldS2)
			oldCrossingsCount := oldCrossingsCount1 + oldCrossingsCount2
			oldOverlapsCount := oldOverlapsCount1 + oldOverlapsCount2
			oldCloseOverlapsCount := oldCloseOverlapsCount1 + oldCloseOverlapsCount2

			newCrossingsCount1, newOverlapsCount1, newCloseOverlapsCount1 := countEdgeIntersects(g, g.Edges[ei], *newS1)
			newCrossingsCount2, newOverlapsCount2, newCloseOverlapsCount2 := countEdgeIntersects(g, g.Edges[ei], *newS1)
			newCrossingsCount := newCrossingsCount1 + newCrossingsCount2
			newOverlapsCount := newOverlapsCount1 + newOverlapsCount2
			newCloseOverlapsCount := newCloseOverlapsCount1 + newCloseOverlapsCount2

			if newCrossingsCount > oldCrossingsCount {
				continue
			}
			if newOverlapsCount > oldOverlapsCount {
				continue
			}

			if newCloseOverlapsCount > oldCloseOverlapsCount {
				continue
			}

			// commit
			g.Edges[ei].Route = append(append(
				e.Route[:i],
				newCorner,
			),
				e.Route[i+3:]...,
			)
			break
		}
	}
}

func countObjectIntersects(g *d2graph.Graph, s geo.Segment) int {
	count := 0
	for _, o := range g.Objects {
		if o.Intersects(s, float64(edge_node_spacing)) {
			count++
		}
	}
	return count
}

// countEdgeIntersects counts both crossings AND getting too close to a parallel segment
func countEdgeIntersects(g *d2graph.Graph, sEdge *d2graph.Edge, s geo.Segment) (int, int, int) {
	isHorizontal := s.Start.Y == s.End.Y
	crossingsCount := 0
	overlapsCount := 0
	closeOverlapsCount := 0
	for _, e := range g.Edges {
		if e == sEdge {
			continue
		}

		for i := 0; i < len(e.Route)-1; i++ {
			otherS := geo.NewSegment(e.Route[i], e.Route[i+1])
			otherIsHorizontal := otherS.Start.Y == otherS.End.Y
			if isHorizontal == otherIsHorizontal {
				if s.Overlaps(*otherS, !isHorizontal, 0.) {
					if isHorizontal {
						if math.Abs(s.Start.Y-otherS.Start.Y) < float64(edge_node_spacing)/2. {
							overlapsCount++
							if math.Abs(s.Start.Y-otherS.Start.Y) < float64(edge_node_spacing)/4. {
								closeOverlapsCount++
							}
						}
					} else {
						if math.Abs(s.Start.X-otherS.Start.X) < float64(edge_node_spacing)/2. {
							overlapsCount++
							if math.Abs(s.Start.X-otherS.Start.X) < float64(edge_node_spacing)/4. {
								closeOverlapsCount++
							}
						}
					}
				}
			} else {
				if s.Intersects(*otherS) {
					crossingsCount++
				}
			}
		}

	}
	return crossingsCount, overlapsCount, closeOverlapsCount
}
