package globaldb

import (
	"testing"
)

func TestDbInterfaceMethods(t *testing.T) {
	t.Run("Check fetching instances", func(t *testing.T) {
		instances, err := testDBService.GetAllInstances()
		if err != nil {
			t.Errorf(err.Error())
			return
		}
		if len(instances) != 0 {
			t.Errorf("unexpected number of instances: %d", len(instances))
		}
	})
}
