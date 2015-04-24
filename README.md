# Goal #
Use bosun.org's mature machine statistics gatherer,
scollector (`bosun.org/scollector`) with prometheus.io's nice slim&sleek Hadoop-free storage.

# Usage #

    go get github.com/tgulacsi/prometheus_scollector
	prometheus_scollector -http=0.0.0.0:9107
	scollector -h=<the-collector-machine>:9107

and don't forget to add a

	job: {
		name: "scollector"
		target_group: {
			target: "http://the-collector-machine:9107/metrics"
		}
	}

to your prometheus.conf.
