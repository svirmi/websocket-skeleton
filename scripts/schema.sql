CREATE TABLE IF NOT EXISTS raw_data (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    data BLOB NOT NULL,
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS processed_data (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    raw_data_id INTEGER,
    processed_data BLOB NOT NULL,
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (raw_data_id) REFERENCES raw_data(id)
);

-- Index for timestamp queries
CREATE INDEX IF NOT EXISTS idx_raw_data_timestamp ON raw_data(timestamp);
CREATE INDEX IF NOT EXISTS idx_processed_data_timestamp ON processed_data(timestamp);