package qldb

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/qldb"
	"github.com/hashicorp/aws-sdk-go-base/v2/awsv1shim/v2/tfawserr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"github.com/hashicorp/terraform-provider-aws/internal/conns"
	tftags "github.com/hashicorp/terraform-provider-aws/internal/tags"
	"github.com/hashicorp/terraform-provider-aws/internal/tfresource"
	"github.com/hashicorp/terraform-provider-aws/internal/verify"
)

// TODO:  Account for fact that streams can become "completed" and may impact future plans if this is not recognized as a valid state that does not need a re-apply to resolve...

func ResourceStream() *schema.Resource {
	return &schema.Resource{
		Create: resourceStreamCreate,
		Read:   resourceStreamRead,
		Update: resourceStreamUpdate,
		Delete: resourceStreamDelete,

		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Schema: map[string]*schema.Schema{
			"arn": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"exclusive_end_time": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
			},
			"inclusive_start_time": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"kinesis_configuration": {
				Type:     schema.TypeList,
				Required: true,
				ForceNew: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"aggregation_enabled": {
							Type:     schema.TypeBool,
							Optional: true,
							Default:  true,
							ForceNew: true,
						},
						"stream_arn": {
							Type:         schema.TypeString,
							Required:     true,
							ForceNew:     true,
							ValidateFunc: verify.ValidARN,
						},
					},
				},
			},
			"ledger_name": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
				ValidateFunc: validation.All(
					validation.StringLenBetween(1, 32),
				),
			},
			"role_arn": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: verify.ValidARN,
			},
			"stream_name": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
				ValidateFunc: validation.All(
					validation.StringLenBetween(1, 32),
				),
			},
			"tags":     tftags.TagsSchema(),
			"tags_all": tftags.TagsSchemaComputed(),
		},
	}
}

func resourceStreamCreate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).QLDBConn
	defaultTagsConfig := meta.(*conns.AWSClient).DefaultTagsConfig
	tags := defaultTagsConfig.MergeTags(tftags.New(d.Get("tags").(map[string]interface{})))

	// Create the QLDB Stream
	createOpts := &qldb.StreamJournalToKinesisInput{
		LedgerName: aws.String(d.Get("ledger_name").(string)),
		RoleArn:    aws.String(d.Get("role_arn").(string)),
		StreamName: aws.String(d.Get("stream_name").(string)),
		Tags:       Tags(tags.IgnoreAWS()),
	}

	if v, ok := d.GetOk("exclusive_end_time"); ok {
		log.Printf("DEBUG - exclusive_end_time: %#v", v)
		exclusiveEndTimeValue, _ := time.Parse("2006-01-02T15:04:05Z", v.(string))
		createOpts.ExclusiveEndTime = &exclusiveEndTimeValue
	}

	if v, ok := d.GetOk("inclusive_start_time"); ok {
		log.Printf("DEBUG - inclusive_start_time: %#v", v)
		inclusiveStartTimeValue, _ := time.Parse("2006-01-02T15:04:05Z", v.(string))
		createOpts.InclusiveStartTime = &inclusiveStartTimeValue
	} else {
		return errors.New("Missing 'inclusive_start_time'")
	}

	if v, ok := d.GetOk("kinesis_configuration"); ok {
		createOpts.KinesisConfiguration = &qldb.KinesisConfiguration{}

		values := v.(map[string]interface{})
		// values := v.(*schema.TypeMap)
		if value, ok := values["aggregation_enabled"]; ok {
			aggregationEnabled, err := strconv.ParseBool(value.(string))
			if err != nil {
				return errors.New("Error parsing kinesis_configuration.aggregation_enabled")
			}
			createOpts.KinesisConfiguration.AggregationEnabled = &aggregationEnabled
		}

		if value, ok := values["stream_arn"]; ok {
			streamArn := value.(string)
			createOpts.KinesisConfiguration.StreamArn = &streamArn
		}
	} else if !ok {
		return errors.New("Missing 'kinesis_configuration'")
	}

	log.Printf("[DEBUG] QLDB Stream create config: %#v", *createOpts)
	qldbResp, err := conn.StreamJournalToKinesis(createOpts)
	if err != nil {
		return fmt.Errorf("Error creating QLDB Ledger Stream: %s", err)
	}

	// Set QLDB Stream ID
	d.SetId(aws.StringValue(qldbResp.StreamId))
	log.Printf("[INFO] QLDB Ledger Stream Id: %s", d.Id())

	stateConf := &resource.StateChangeConf{
		Pending:    []string{qldb.StreamStatusImpaired},
		Target:     []string{qldb.StreamStatusActive},
		Refresh:    qldbStreamRefreshStatusFunc(conn, d.Get("ledger_name").(string), d.Id()),
		Timeout:    8 * time.Minute,
		MinTimeout: 3 * time.Second,
	}

	_, err = stateConf.WaitForState()
	if err != nil {
		return fmt.Errorf("Error waiting for QLDB Ledger status to be \"%s\": %s", qldb.LedgerStateActive, err)
	}

	// Update our attributes and return
	return resourceStreamRead(d, meta)
}

//TODO: Another edge case to account for?:  Stream resources that are in a terminal state (CANCELED, COMPLETED, and FAILED) are subject to a 7-day retention period. They are automatically hard-deleted after this limit expires.
func resourceStreamRead(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).QLDBConn
	defaultTagsConfig := meta.(*conns.AWSClient).DefaultTagsConfig
	ignoreTagsConfig := meta.(*conns.AWSClient).IgnoreTagsConfig

	var ledgerName string
	if ledgerNameValue, ok := d.GetOk("ledger_name"); ok {
		ledgerName = ledgerNameValue.(string)
	}

	// Refresh the QLDB Stream state
	input := &qldb.DescribeJournalKinesisStreamInput{
		LedgerName: aws.String(ledgerName),
		StreamId:   aws.String(d.Id()),
	}

	qldbStream, err := conn.DescribeJournalKinesisStream(input)

	if tfawserr.ErrMessageContains(err, qldb.ErrCodeResourceNotFoundException, "") {
		log.Printf("[WARN] QLDB Stream (%s) not found, removing from state", d.Id())
		d.Set("ledger_name", "")
		return nil
	}

	log.Printf("DEBUG - QLDB Stream: %#v", qldbStream)

	if err != nil {
		return fmt.Errorf("error describing QLDB Stream (%s): %s", d.Id(), err)
	}

	if err := d.Set("arn", qldbStream.Stream.Arn); err != nil {
		return fmt.Errorf("error setting ARN: %s", err)
	}

	if qldbStream.Stream.ExclusiveEndTime != nil {
		if err := d.Set("exclusive_end_time", qldbStream.Stream.ExclusiveEndTime.Format("2006-01-02T15:04:05Z")); err != nil {
			return fmt.Errorf("error setting Exclusive End Time: %s", err)
		}
	}

	if err := d.Set("inclusive_start_time", qldbStream.Stream.InclusiveStartTime.Format("2006-01-02T15:04:05Z")); err != nil {
		return fmt.Errorf("error setting Inclusive Start Time: %s", err)
	}

	if qldbStream.Stream.KinesisConfiguration != nil {
		kinesisConfiguration := map[string]interface{}{
			"aggregation_enabled": strconv.FormatBool(*qldbStream.Stream.KinesisConfiguration.AggregationEnabled),
			"stream_arn":          *qldbStream.Stream.KinesisConfiguration.StreamArn,
		}

		if err := d.Set("kinesis_configuration", kinesisConfiguration); err != nil {
			return fmt.Errorf("error setting Kinesis Configuration: %s", err)
		}
	}

	if err := d.Set("ledger_name", qldbStream.Stream.LedgerName); err != nil {
		return fmt.Errorf("error setting Ledger Name: %s", err)
	}

	if err := d.Set("role_arn", qldbStream.Stream.RoleArn); err != nil {
		return fmt.Errorf("error setting Role Arn: %s", err)
	}

	if err := d.Set("stream_name", qldbStream.Stream.StreamName); err != nil {
		return fmt.Errorf("error setting Stream Name: %s", err)
	}

	log.Printf("[INFO] Fetching tags for %s", d.Id())
	tags, err := ListTags(conn, d.Get("arn").(string))
	if err != nil {
		return fmt.Errorf("Error listing tags for QLDB Stream: %s", err)
	}

	tags = tags.IgnoreAWS().IgnoreConfig(ignoreTagsConfig)

	if err != nil {
		return fmt.Errorf("Error listing tags for QLDB Stream: %s", err)
	}

	if err := d.Set("tags", tags.RemoveDefaultConfig(defaultTagsConfig).Map()); err != nil {
		return fmt.Errorf("error setting tags: %w", err)
	}

	return nil
}

func resourceStreamUpdate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).QLDBConn

	if d.HasChange("tags") {
		o, n := d.GetChange("tags")
		if err := UpdateTags(conn, d.Get("arn").(string), o, n); err != nil {
			return fmt.Errorf("error updating tags: %s", err)
		}
	}

	return resourceStreamRead(d, meta)
}

func resourceStreamDelete(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).QLDBConn
	deleteQLDBStreamOpts := &qldb.CancelJournalKinesisStreamInput{
		LedgerName: aws.String(d.Get("ledger_name").(string)),
		StreamId:   aws.String(d.Id()),
	}
	log.Printf("[INFO] Cancelling QLDB Ledger: %s %s", d.Get("ledger_name"), d.Id())

	err := resource.Retry(5*time.Minute, func() *resource.RetryError {
		_, err := conn.CancelJournalKinesisStream(deleteQLDBStreamOpts)

		if tfawserr.ErrMessageContains(err, qldb.ErrCodeResourceInUseException, "") {
			return resource.RetryableError(err)
		}

		if err != nil {
			return resource.NonRetryableError(err)
		}

		return nil
	})

	if tfresource.TimedOut(err) {
		_, err = conn.CancelJournalKinesisStream(deleteQLDBStreamOpts)
	}

	if tfawserr.ErrMessageContains(err, qldb.ErrCodeResourceNotFoundException, "") {
		return nil
	}

	if err != nil {
		return fmt.Errorf("error cancelling QLDB Stream (%s, %s): %s", d.Get("ledger_name"), d.Id(), err)
	}

	if err := waitForQLDBStreamDeletion(conn, d.Get("ledger_name").(string), d.Id()); err != nil {
		return fmt.Errorf("error waiting for QLDB Stream (%s, %s) deletion: %s", d.Get("ledger_name"), d.Id(), err)
	}

	return err
}

func qldbStreamRefreshStatusFunc(conn *qldb.QLDB, ledgerName string, streamID string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		input := &qldb.DescribeJournalKinesisStreamInput{
			LedgerName: aws.String(ledgerName),
			StreamId:   aws.String(streamID),
		}
		resp, err := conn.DescribeJournalKinesisStream(input)
		if err != nil {
			return nil, "failed", err
		}
		return resp, aws.StringValue(resp.Stream.Status), nil
	}
}

func waitForQLDBStreamDeletion(conn *qldb.QLDB, ledgerName string, streamID string) error {
	stateConf := resource.StateChangeConf{
		Pending: []string{
			qldb.StreamStatusActive,
			qldb.StreamStatusImpaired,
		},
		Target: []string{
			qldb.StreamStatusCompleted,
			qldb.StreamStatusFailed,
			qldb.StreamStatusCanceled,
		},
		Timeout:    5 * time.Minute,
		MinTimeout: 1 * time.Second,
		Refresh: func() (interface{}, string, error) {
			resp, err := conn.DescribeJournalKinesisStream(&qldb.DescribeJournalKinesisStreamInput{
				LedgerName: aws.String(ledgerName),
				StreamId:   aws.String(streamID),
			})

			if tfawserr.ErrMessageContains(err, qldb.ErrCodeResourceNotFoundException, "") {
				return 1, "", nil
			}

			if err != nil {
				return nil, qldb.ErrCodeResourceInUseException, err
			}

			return resp, aws.StringValue(resp.Stream.Status), nil
		},
	}

	_, err := stateConf.WaitForState()

	return err
}

func FindJournalKinesisStream(ctx context.Context, conn *qldb.QLDB, ledgerName, streamID string) (*qldb.JournalKinesisStreamDescription, error) {
	input := &qldb.DescribeJournalKinesisStreamInput{
		LedgerName: aws.String(ledgerName),
		StreamId:   aws.String(streamID),
	}

	output, err := conn.DescribeJournalKinesisStreamWithContext(ctx, input)

	if tfawserr.ErrCodeEquals(err, qldb.ErrCodeResourceNotFoundException) {
		return nil, &resource.NotFoundError{
			LastError:   err,
			LastRequest: input,
		}
	}

	if err != nil {
		return nil, err
	}

	if output == nil || output.Stream == nil {
		return nil, tfresource.NewEmptyResultError(input)
	}

	return output.Stream, nil
}
