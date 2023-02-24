package ec2_test

import (
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go/service/ec2"
	sdkacctest "github.com/hashicorp/terraform-plugin-sdk/v2/helper/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-provider-aws/internal/acctest"
)

func testAccTransitGatewayRouteTablePropagationsDataSource_basic(t *testing.T) {
	ctx := acctest.Context(t)
	rName := sdkacctest.RandomWithPrefix(acctest.ResourcePrefix)
	dataSourceName := "data.aws_ec2_transit_gateway_route_table_propagations.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t); testAccPreCheckTransitGateway(ctx, t) },
		ErrorCheck:               acctest.ErrorCheck(t, ec2.EndpointsID),
		ProtoV5ProviderFactories: acctest.ProtoV5ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccTransitGatewayRouteTablePropagationsDataSourceConfig_basic(rName),
				Check: resource.ComposeTestCheckFunc(
					acctest.CheckResourceAttrGreaterThanValue(dataSourceName, "ids.#", "0"),
				),
			},
		},
	})
}

func testAccTransitGatewayRouteTablePropagationsDataSourceConfig_basic(rName string) string {
	return fmt.Sprintf(`
resource "aws_ec2_transit_gateway" "test" {
  tags = {
    Name = %[1]q
  }
}
resource "aws_vpc" "test" {
	cidr_block = "10.1.0.0/16"
  
	tags = {
	  Name = %[1]q
	}
  }
  
resource "aws_subnet" "test" {
	cidr_block = "10.1.1.0/24"
	vpc_id     = aws_vpc.test.id
}  
resource "aws_ec2_transit_gateway_vpc_attachment" "test" {
	subnet_ids         = [aws_subnet.test.id]
	transit_gateway_id = aws_ec2_transit_gateway.test.id
	vpc_id             = aws_vpc.test.id
}
resource "aws_ec2_transit_gateway_route_table" "test" {
  transit_gateway_id = aws_ec2_transit_gateway.test.id
  tags = {
    Name = %[1]q
  }
}
resource "aws_ec2_transit_gateway_route_table_propagation" "test" {
	transit_gateway_attachment_id  = aws_ec2_transit_gateway_vpc_attachment.test.id
	transit_gateway_route_table_id = aws_ec2_transit_gateway_route_table.test.id
	tags = {
		Name = %[1]q
	  }
	}
data "aws_ec2_transit_gateway_route_table_propagations" "test" {
	transit_gateway_route_table_id = aws_ec2_transit_gateway_route_table.test.id
	depends_on = [aws_ec2_transit_gateway_route_table_propagation.test]
}
`, rName)
}
