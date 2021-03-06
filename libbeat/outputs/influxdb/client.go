package influxdb

import (
	"fmt"
	influxdb "github.com/influxdata/influxdb/client/v2"
	"time"

	"github.com/elastic/beats/libbeat/logp"
	"github.com/elastic/beats/libbeat/outputs"
	"github.com/elastic/beats/libbeat/publisher"
)

type client struct {
	conn          influxdb.Client
	stats         *outputs.Stats
	addr          string
	username      string
	password      string
	db            string
	measurement   string
	timePrecision string
	tagFields     []string
	tagFieldsHash map[string]int
	timeField     string
}

func newClient(
	stats *outputs.Stats,
	addr string,
	user string,
	pass string,
	db string,
	measurement string,
	timePrecision string,
	tagFields []string,
	timeField string,
) *client {

	hash := make(map[string]int)
	for _, f := range tagFields {
		if f != "" {
			hash[f] = 1
		}
	}

	return &client{
		stats:         stats,
		addr:          addr,
		username:      user,
		password:      pass,
		db:            db,
		measurement:   measurement,
		timePrecision: timePrecision,
		tagFields:     tagFields,
		tagFieldsHash: hash,
		timeField:     timeField,
	}
}

func (c *client) Connect() error {
	var err error
	debugf("connect")

	c.conn, err = influxdb.NewHTTPClient(influxdb.HTTPConfig{
		Addr:     c.addr,
		Username: c.username,
		Password: c.password,
	})
	if err != nil {
		logp.Err("Failed to create HTTP conn to influxdb: %v", err)
		return err
	}

	logp.Info("Client to influxdb has created: %v", c.addr)

	return err
}

func (c *client) Close() error {
	debugf("close connection")
	return c.conn.Close()
}

func (c *client) Publish(batch publisher.Batch) error {
	if c == nil {
		panic("no client")
	}
	if batch == nil {
		panic("no batch")
	}

	events := batch.Events()
	c.stats.NewBatch(len(events))
	failed, err := c.publish(events)
	if err != nil {
		logp.Err("publish failed:", err)
		batch.RetryEvents(failed)
		c.stats.Failed(len(failed))
		return err
	}
	batch.ACK()
	return nil
}

func (c *client) publish(data []publisher.Event) ([]publisher.Event, error) {
	var err error

	serialized := c.serializeEvents(data)

	dropped := len(data) - len(serialized)
	c.stats.Dropped(dropped)
	if dropped > 0 {
		logp.Info("Number of dropped points: %v/%v", dropped, len(data))
	}

	if (len(serialized)) == 0 {
		return nil, nil
	}

	bp, _ := influxdb.NewBatchPoints(influxdb.BatchPointsConfig{
		Database:  c.db,
		Precision: c.timePrecision,
	})

	for i := 0; i < len(serialized); i++ {
		pt := serialized[i]
		bp.AddPoint(pt)
	}

	err = c.conn.Write(bp)

	if err != nil {
		logp.Err("Failed to write to influxdb: %v", err)
		for _, event := range data[:len(serialized)] {
                        logp.Info("Content: ", event.Content)
                }
		return data[:len(serialized)], err

	}

	c.stats.Acked(len(serialized))
	return nil, nil
}

func (c *client) scanFields(originFields map[string]interface{}) (map[string]string, map[string]interface{}) {
	tags := make(map[string]string)
	fields := make(map[string]interface{})

	for k, _ := range originFields {
		_, ok := c.tagFieldsHash[k]
		if !ok {
			fields[k] = originFields[k]
			continue
		}

		// This field is a tag, need to check wether is a string
		switch v := originFields[k].(type) {
		case string:
			tags[k] = v
		case int, int8, int16, int32, int64:
			tags[k] = fmt.Sprintf("%d", v)
		default:
			logp.Warn("Unsupported tag type: %v(%T)", v, v)
		}
	}

	return tags, fields

}

func (c *client) serializeEvents(
	data []publisher.Event,
) []*influxdb.Point {
	to := make([]*influxdb.Point, 0, len(data))

	for _, d := range data {
		t := d.Content.Timestamp
		if timestamp, ok := d.Content.Fields[c.timeField]; ok {
			if v, ok := timestamp.(int64); ok {
				if c.timePrecision == "s" {
					t = time.Unix(v, 0)
				} else if c.timePrecision == "ms" {
					t = time.Unix(v/1000, (v%1000)*1000*1000)
				} else {
					logp.Warn("Unsupported time precision: %v", c.timePrecision)
				}
			}
		}

		tags, fields := c.scanFields(d.Content.Fields)

		point, err := influxdb.NewPoint(c.measurement, tags, fields, t)
		if err != nil {
			logp.Err("Encoding event failed with error: %v", err)
			goto end
		}

		to = append(to, point)
	}

end:
	return to
}
