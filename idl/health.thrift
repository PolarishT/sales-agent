namespace go health

struct HealthRequest {}

struct HealthResponse {
    1: required string Status (go.tag="json:\"status\"")
    2: required string Code (go.tag="json:\"code\"")
}

struct ErrorResponse {
    1: required string Status (go.tag="json:\"status\"")
    2: required string Code (go.tag="json:\"code\"")
    3: required string Message (go.tag="json:\"message\"")
    4: required string RequestID (go.tag="json:\"request_id\"")
}

service HealthService {
    HealthResponse Live(1: HealthRequest request) (api.get="/api/v1/health/live")
    HealthResponse Ready(1: HealthRequest request) (api.get="/api/v1/health/ready")
}
