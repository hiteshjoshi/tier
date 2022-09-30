name: "test"

on: pull_request: branches: ["*"]

jobs: {
	test: {
		strategy: matrix: "runs-on": [
			"buildjet-2vcpu-ubuntu-2204",
			"buildjet-4vcpu-ubuntu-2204",
			"buildjet-8vcpu-ubuntu-2204",
			"buildjet-16vcpu-ubuntu-2204",
		]

		name:      "Test on ${{ matrix.runs-on }}"
		"runs-on": "${{ matrix.runs-on }}"
		steps: [{
			name: "Checkout"
			uses: "actions/checkout@v3"
		}, {
			name: "Setup Go"
			uses: "actions/setup-go@v3"
			with: {
				"go-version": "1.19"
			}
		}, {
			name: "Go Test"
			env: STRIPE_API_KEY: "${{ secrets.STRIPE_API_KEY }}"
			run: """
				go test -count=1 -v ./...
				"""
		}]
	}
}
