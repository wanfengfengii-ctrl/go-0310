package domain

// MeasurementRequest is a single point reading submitted by an operator or a
// scripted acquisition adapter. AtMillis may be zero, in which case the engine
// stamps the current logical time.
type MeasurementRequest struct {
	Stage                  StageName `json:"stage"`
	Cycle                  int       `json:"cycle"`
	SensorID               string    `json:"sensor_id"`
	TemperatureMilliKelvin int64     `json:"temperature_milli_kelvin"`
	PressureMilliPa        int64     `json:"pressure_milli_pa"`
	CollectorID            string    `json:"collector_id"`
	LeaseToken             string    `json:"lease_token"`
	AtMillis               int64     `json:"at_millis,omitempty"`
	// Generation, when non-zero, must match the run's current generation.
	// A lower value marks a late reading from an older retest generation,
	// which is archived rather than admitted to the valid evidence index.
	Generation int `json:"generation,omitempty"`
}
