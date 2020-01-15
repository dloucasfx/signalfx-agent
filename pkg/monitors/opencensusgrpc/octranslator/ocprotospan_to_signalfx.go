package octranslator

import (
	"fmt"
	"net"
	"strconv"

	tracepb "github.com/census-instrumentation/opencensus-proto/gen-go/trace/v1"
	"github.com/golang/protobuf/ptypes/timestamp"
	"github.com/open-telemetry/opentelemetry-collector/consumer/consumerdata"
	octracetranslator "github.com/open-telemetry/opentelemetry-collector/translator/trace"
	"github.com/signalfx/golib/pointer"
	sfxtrace "github.com/signalfx/golib/trace"
)

const (
	statusCodeTagKey        = "error"
	statusDescriptionTagKey = "opencensus.status_description"

	//zipkin constants
	LocalEndpointIPv4         = "ipv4"
	LocalEndpointIPv6         = "ipv6"
	LocalEndpointPort         = "port"
	RemoteEndpointIPv4        = "zipkin.remoteEndpoint.ipv4"
	RemoteEndpointIPv6        = "zipkin.remoteEndpoint.ipv6"
	RemoteEndpointPort        = "zipkin.remoteEndpoint.port"
	RemoteEndpointServiceName = "zipkin.remoteEndpoint.serviceName"
)

var canonicalCodes = [...]string{
		"OK",
		"CANCELLED",
		"UNKNOWN",
		"INVALID_ARGUMENT",
		"DEADLINE_EXCEEDED",
		"NOT_FOUND",
		"ALREADY_EXISTS",
		"PERMISSION_DENIED",
		"RESOURCE_EXHAUSTED",
		"FAILED_PRECONDITION",
		"ABORTED",
		"OUT_OF_RANGE",
		"UNIMPLEMENTED",
		"INTERNAL",
		"UNAVAILABLE",
		"DATA_LOSS",
		"UNAUTHENTICATED",
	}

// convertTraceID converts hex bytes into a hex string
func convertTraceID(t []byte) string {
	// the conversion to uint64 is done in the otel zipkin exporter
	h, l, _ := octracetranslator.BytesToUInt64TraceID(t[:])

	// the following string conversion is modeled after the JSON Marshal method on zipkin.model.TraceID
	if h == 0 {
		return fmt.Sprintf("%016x", l)
	}
	return fmt.Sprintf("%016x%016x", h, l)
}

// convertSpanID converts an OC SpanID to a hex string
func convertSpanID(s []byte) string {
	// the conversion to unit64 is done in the otel zipkin exporter
	id, _ := octracetranslator.BytesToUInt64SpanID(s[:])
	// the string conversion is modeled after the JSON Marshal method on zipkin.model.ID
	return fmt.Sprintf("%016x", uint64(id))
}

// truncatableStringToString converts an OC TruncatableString to a string pointer
func truncatableStringToString(ts *tracepb.TruncatableString) *string {
	if ts == nil {
		return nil
	}

	return pointer.String(ts.Value)
}

// spanKindToString converts an OC SpanKind to a string pointer
func spanKindToString(s tracepb.Span_SpanKind) *string {
	switch s {
	case tracepb.Span_CLIENT:
		return pointer.String("CLIENT")
	case tracepb.Span_SERVER:
		return pointer.String("SERVER")
	default:
		return nil
	}
}

// timestampToMicroseconds converts a protobuf timestamp to microseconds
func timestampToMicroseconds(ts *timestamp.Timestamp) *int64 {
	if ts == nil {
		return nil
	}
	micros := ts.Seconds*1000000 + int64(ts.Nanos/1000)
	return &micros
}

// attributeValueToString converts an OC AttributeValue to a string
func attributeValueToString(attr *tracepb.AttributeValue) (string, bool) {
	if attr == nil || attr.Value == nil {
		return "", false
	}
	switch value := attr.Value.(type) {
	case *tracepb.AttributeValue_BoolValue:
		if value.BoolValue {
			return "true", true
		} else {
			return "false", true
		}

	case *tracepb.AttributeValue_IntValue:
		return strconv.FormatInt(value.IntValue, 10), true

	case *tracepb.AttributeValue_StringValue:
		if value.StringValue != nil {
			return value.StringValue.Value, true
		}
	default:
	}
	return "", false
}

// attributesToTags converts a map of OC Span Attributes to a map[string]string
func attributesToTags(redundantKeys map[string]bool, attrMap map[string]*tracepb.AttributeValue) map[string]string {
	if len(attrMap) == 0 {
		return nil
	}
	// construct Tags from s.Attributes and s.Status.
	m := make(map[string]string, len(attrMap)+2)
	for key, value := range attrMap {
		if redundantKeys[key] {
			// Already represented by something other than an attribute,
			// skip it.
			continue
		}

		// convert the attribute value to string
		if strVal, ok := attributeValueToString(value); ok {
			// nil attribute values are checked in attributeValueToString
			m[key] = strVal
		}
	}
	return m

}

// getStringAttribute retrieves looks up an OC Attribute key and returns a string representation of an OC AttributeValue
func getStringAttribute(attributes map[string]*tracepb.AttributeValue, key string) (value string, ok bool) {
	if val, isIn := attributes[key]; isIn {
		value, ok = attributeValueToString(val)
	}

	return value, ok
}

// getEndpointFromAttributes returns an endpoint from a set of OC Span_Attributes.  It creates an endpoint by looking up
// the specified ipv4, ipv6, and port OC Span Attributes keys.
func getEndpointFromAttributes(attributes *tracepb.Span_Attributes, serviceName string, redundantKeys map[string]bool, ipv4Key string, ipv6Key string, portKey string) *sfxtrace.Endpoint {
	if attributes == nil {
		return nil
	}

	// extract port
	var port uint64
	// NOTE: The OTel Zipkin exporter explicitly does this conversion of the port attribute value to a string.
	// I'm not sure what format the port is in inside the OC AttributesMap.  This conversion is safer,
	// but may be inefficient.
	if portStr, ok := getStringAttribute(attributes.AttributeMap, portKey); ok {
		redundantKeys[portKey] = true
		port, _ = strconv.ParseUint(portStr, 10, 16)
	}

	// extract ipv4
	if ipv4Str, ok := getStringAttribute(attributes.AttributeMap, ipv4Key); ok {
		redundantKeys[ipv4Key] = true
		ip := net.ParseIP(ipv4Str)

		// return nil if there was an ipv4 key but no information
		if serviceName != "" && len(ip) > 0 && port > 0 {
			// return the ipv4 endpoint
			return &sfxtrace.Endpoint{
				ServiceName: pointer.String(serviceName),
				Port:        pointer.Int32(int32(port)),
				Ipv4:        pointer.String(ip.String()),
			}
		}
	} else if ipv6Str, ok := getStringAttribute(attributes.AttributeMap, ipv6Key); ok {
		redundantKeys[ipv6Key] = true
		ip := net.ParseIP(ipv6Str)

		// return the ipv6 endpoint
		if serviceName != "" && len(ip) > 0 && port > 0 {
			return &sfxtrace.Endpoint{
				ServiceName: pointer.String(serviceName),
				Port:        pointer.Int32(int32(port)),
				Ipv6:        pointer.String(ip.String()),
			}
		}
	}

	return nil
}

// spanTimeEventMessageEventTypeToString converts the OC MessageEvent TimeEvent message type to a string pointer
func spanTimeEventMessageEventTypeToString(t tracepb.Span_TimeEvent_MessageEvent_Type) *string {
	// message
	switch t {
	case tracepb.Span_TimeEvent_MessageEvent_SENT:
		return pointer.String("SENT")
	case tracepb.Span_TimeEvent_MessageEvent_RECEIVED:
		return pointer.String("RECV")
	default:
		return pointer.String("<?>")
	}
}

// timeEventsToAnnotations converts Annotations and MessageEvent OC TimeEvents to SFX Annotations
func timeEventsToAnnotations(tes *tracepb.Span_TimeEvents) []*sfxtrace.Annotation {
	if tes == nil || len(tes.TimeEvent) == 0 {
		return nil
	}

	annotations := make([]*sfxtrace.Annotation, 0, len(tes.TimeEvent))
	for _, te := range tes.TimeEvent {
		if te == nil || te.Value == nil {
			continue
		}

		var annotation sfxtrace.Annotation
		switch ann := te.Value.(type) {
		case *tracepb.Span_TimeEvent_Annotation_:
			// oc annotation
			if ann.Annotation == nil {
				continue
			}
			annotation.Value = truncatableStringToString(ann.Annotation.GetDescription())
		case *tracepb.Span_TimeEvent_MessageEvent_:
			// oc message event
			if ann.MessageEvent == nil {
				continue
			}
			annotation.Value = spanTimeEventMessageEventTypeToString(ann.MessageEvent.GetType())
		default:
			continue
		}

		// add timestamp
		annotation.Timestamp = timestampToMicroseconds(te.Time)

		// add the annotation
		annotations = append(annotations, &annotation)
	}

	return annotations
}

// getDurationInMicrosecondsFromTimestamps
func getDurationInMicrosecondsFromTimestamps(start *timestamp.Timestamp, end *timestamp.Timestamp) *int64 {
	var dur int64
	if start != nil && (start.Seconds > 0 || start.Nanos > 0) && end != nil && (end.Seconds > 0 || end.Nanos > 0){
		dur = ((end.Seconds - start.Seconds)*1000000) + int64((end.Nanos - end.Nanos)/1000)
	}
	return &dur
}

// OCProtoSpansToSignalFx converts oc protospan
func OCProtoSpansToSignalFx(td consumerdata.TraceData) []*sfxtrace.Span {
	serviceName := td.Node.GetServiceInfo().GetName()
	spans := make([]*sfxtrace.Span, len(td.Spans))
	for _, s := range td.Spans {
		if s != nil {
			spans = append(spans, OCProtoSpanToSignalFx(serviceName, s))
		}
	}
	return spans
}

func OCProtoSpanToSignalFx(serviceName string, s *tracepb.Span) *sfxtrace.Span {
	// Some things (i.e. ip or port) are stored as attributes.  These special attributes are extracted and used
	// before the span attributes are converted to tags. This map keeps track of the keys that
	// are used so that they can be skipped when converting all of the attributes to tags.
	extractedAttributeKeys := make(map[string]bool, 6)

	// extract the remote service name
	remoteServiceName, ok := getStringAttribute(s.Attributes.AttributeMap, RemoteEndpointServiceName)
	if ok {
		extractedAttributeKeys[RemoteEndpointServiceName] = true
	}

	// NOTE: currently the trace trace state on the OC span gets dropped.  This is because there is no trace state in
	// SignalFx (and zipkinV2).  Some OTel translators do attempt to extract trace state entries, but there is not a
	// well defined way to handle this when converting to SignalFx (and zipkinV2).

	z := &sfxtrace.Span{
		TraceID:        convertTraceID(s.TraceId),
		ID:             convertSpanID(s.SpanId),
		Kind:           spanKindToString(s.Kind),
		Name:           truncatableStringToString(s.Name),
		Timestamp:      timestampToMicroseconds(s.StartTime),
		Duration: 		getDurationInMicrosecondsFromTimestamps(s.StartTime, s.EndTime),
		Shared:         pointer.Bool(false),
		Annotations:    timeEventsToAnnotations(s.TimeEvents),
		LocalEndpoint:  getEndpointFromAttributes(s.Attributes, serviceName, extractedAttributeKeys, LocalEndpointIPv4, LocalEndpointIPv6, LocalEndpointPort),
		RemoteEndpoint: getEndpointFromAttributes(s.Attributes, remoteServiceName, extractedAttributeKeys, RemoteEndpointIPv4, RemoteEndpointIPv6, RemoteEndpointPort),
	}

	// convert parent span id
	if len(s.ParentSpanId) > 0 {
		z.ParentID = pointer.String(convertSpanID(s.ParentSpanId))
	}

	// convert attributes
	if s.Attributes != nil {
		z.Tags = attributesToTags(extractedAttributeKeys, s.Attributes.AttributeMap)
	}

	// status code
	if s.Status.Code != 0 || s.Status.Message != "" {
		if z.Tags == nil {
			z.Tags = make(map[string]string, 2)
		}
		if s.Status.Code != 0 {
			if s.Status.Code < 0 || int(s.Status.Code) >= len(canonicalCodes) {
				z.Tags[statusCodeTagKey] = "error code " + strconv.FormatInt(int64(s.Status.Code), 10)
			} else {
				z.Tags[statusCodeTagKey] = canonicalCodes[s.Status.Code]
			}
		}
		if s.Status.Message != "" {
			z.Tags[statusDescriptionTagKey] = s.Status.Message
		}
	}

	return z
}
