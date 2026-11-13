// swift-tools-version: 5.10
import PackageDescription

let package = Package(
    name: "APIClientCheck",
    platforms: [.macOS(.v10_15)],
    dependencies: [
        .package(url: "https://github.com/apple/swift-openapi-generator", exact: "1.10.4"),
        .package(url: "https://github.com/apple/swift-openapi-runtime", exact: "1.8.2"),
        .package(url: "https://github.com/apple/swift-http-types", exact: "1.3.0"),
    ],
    targets: [
        .executableTarget(
            name: "APIClientCheck",
            dependencies: [
                .product(name: "OpenAPIRuntime", package: "swift-openapi-runtime"),
                .product(name: "HTTPTypes", package: "swift-http-types"),
            ],
            plugins: [.plugin(name: "OpenAPIGenerator", package: "swift-openapi-generator")]
        )
    ]
)
