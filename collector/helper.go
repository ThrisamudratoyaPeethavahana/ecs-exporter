package collector

import (
	"strings"
    "strconv"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/applicationautoscaling"
)

func getAWSRegionForCluster(clusterArn string) string {
    return strings.Split(clusterArn, ":")[3]
}

func getServiceName(resourceId string) string {
    return strings.Split(resourceId, "/")[2]
}

func getScalableTargets(e *ApplicationAutoScalingClient, services []*ecs.Service, clusterName string) (map[string][]string, error) {
	var ResourceIds []*string
	for _,s := range services {
			ResourceIds = append(ResourceIds, aws.String("service/"+clusterName+"/"+aws.StringValue(s.ServiceName)))
	}

	params := &applicationautoscaling.DescribeScalableTargetsInput{
			ServiceNamespace: aws.String("ecs"),
			ResourceIds:      ResourceIds,
	}

	resp, err := e.client.DescribeScalableTargets(params)
	if err != nil {
		return make(map[string][]string), err
	}
	
	return processScalableTargetsForServices(resp.ScalableTargets), nil
}

func processScalableTargetsForServices(services []*applicationautoscaling.ScalableTarget) map[string][]string {
	scalableTargets := make(map[string][]string)
	for _, s := range services {
		serviceName := getServiceName(aws.StringValue(s.ResourceId))
		scalableTargets[serviceName] = []string{
			strconv.Itoa(int(aws.Int64Value(s.MinCapacity))),
			strconv.Itoa(int(aws.Int64Value(s.MaxCapacity))),
		}
	}
	return scalableTargets
}

