package v1alpha1

type WekaContainerBuilder struct {
	WekaVersion string `json:"weka_version,omitempty"`
}

func (c *WekaContainerSpec) GetBuilder() *WekaContainerBuilder {
	if c.Builder == nil {
		return &WekaContainerBuilder{}
	} else {
		return c.Builder
	}
}
