-- +goose Up

ALTER TABLE bridge_last_value ALTER finished_at TYPE timestamp with time zone;


-- +goose Down
ALTER TABLE bridge_last_value ALTER finished_at TYPE timestamp;
