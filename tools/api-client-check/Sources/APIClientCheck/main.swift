import Foundation
import HTTPTypes
import OpenAPIRuntime

struct NoOpTransport: ClientTransport {
    func send(
        _ request: HTTPRequest,
        body: HTTPBody?,
        baseURL: URL,
        operationID: String
    ) async throws -> (HTTPResponse, HTTPBody?) {
        (HTTPResponse(status: .noContent), nil)
    }
}

let client = Client(serverURL: URL(string: "http://localhost/api/v1")!, transport: NoOpTransport())
let operation: (Operations.auth_period_identity.Input) async throws -> Operations.auth_period_identity.Output = client.auth_period_identity
_ = operation
