package pingtop

import "math"

func diagnoseCycle(results []CheckResult, config AppConfig) DiagnosisAssessment {
	if len(config.Targets) == 0 {
		return DiagnosisAssessment{
			Key:              "no_targets",
			ConfirmedMessage: "No targets configured",
			SuspectedMessage: "no targets configured",
		}
	}
	if len(results) == 0 {
		return DiagnosisAssessment{
			Key:              "waiting",
			ConfirmedMessage: "Waiting for first cycle",
			SuspectedMessage: "waiting for first cycle",
		}
	}

	failures := make([]CheckResult, 0)
	ipResults := make([]CheckResult, 0)
	hostResults := make([]CheckResult, 0)
	ipSuccesses := 0
	ipFailures := 0
	hostDNSFailures := 0
	hostReachabilityFailures := 0

	for _, result := range results {
		if result.IsFailure() {
			failures = append(failures, result)
		}
		if result.TargetType == "ip" {
			ipResults = append(ipResults, result)
			if result.PingSuccess {
				ipSuccesses++
			} else {
				ipFailures++
			}
			continue
		}
		hostResults = append(hostResults, result)
		if result.DNSSuccess != nil && !*result.DNSSuccess {
			hostDNSFailures++
		} else if result.DNSSuccess != nil && *result.DNSSuccess && !result.PingSuccess {
			hostReachabilityFailures++
		}
	}

	if len(failures) == 0 {
		return DiagnosisAssessment{
			Key:              "healthy",
			ConfirmedMessage: "All monitored targets are reachable",
			SuspectedMessage: "all monitored targets are reachable",
		}
	}
	if len(ipResults) >= 2 {
		networkThreshold := int(math.Max(2, math.Ceil(float64(len(ipResults))*0.75)))
		if ipFailures >= networkThreshold {
			return DiagnosisAssessment{
				Key:              "network_issue",
				ConfirmedMessage: "Likely general network issue",
				SuspectedMessage: "general network issue",
			}
		}
	}
	if len(hostResults) >= 2 {
		dnsThreshold := int(math.Max(2, math.Ceil(float64(len(hostResults))*0.75)))
		if ipSuccesses > 0 && hostDNSFailures >= dnsThreshold {
			return DiagnosisAssessment{
				Key:              "dns_issue",
				ConfirmedMessage: "Likely DNS issue",
				SuspectedMessage: "DNS issue",
			}
		}
	}
	if len(failures) == 1 && len(results) > 1 {
		return DiagnosisAssessment{
			Key:              "isolated_issue",
			ConfirmedMessage: "Likely isolated target or path issue",
			SuspectedMessage: "isolated target or path issue",
		}
	}
	if len(hostResults) >= 2 {
		reachabilityThreshold := int(math.Max(2, math.Ceil(float64(len(hostResults))*0.75)))
		if hostReachabilityFailures > 0 && hostDNSFailures == 0 && ipSuccesses > 0 {
			if hostReachabilityFailures >= reachabilityThreshold {
				if hostReachabilityFailures == len(hostResults) {
					return DiagnosisAssessment{
						Key:              "host_reachability_all",
						ConfirmedMessage: "DNS okay, but resolved hosts are not reachable",
						SuspectedMessage: "resolved hosts are not reachable even though DNS is working",
					}
				}
				return DiagnosisAssessment{
					Key:              "host_reachability_some",
					ConfirmedMessage: "DNS okay, reachability failed for multiple host targets",
					SuspectedMessage: "host reachability issue after successful DNS resolution",
				}
			}
		}
	}
	return DiagnosisAssessment{
		Key:              "mixed_failures",
		ConfirmedMessage: "Mixed failures across monitored targets",
		SuspectedMessage: "mixed failures across monitored targets",
	}
}
