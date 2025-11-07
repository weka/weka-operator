package resources

import (
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/util"
)

func GetWekaClientContainerName(wekaClient *weka.WekaClient) string {
	return fmt.Sprintf("%sclient", util.GetLastGuidPart(wekaClient.GetUID()))
}
