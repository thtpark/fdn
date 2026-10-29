package foundation

const observationSchema = `
CREATE INDEX messages_received_time ON messages(recipient_id,json_extract(data,'$.created_at') DESC,id DESC);
CREATE INDEX messages_sent_time ON messages(sender_id,json_extract(data,'$.created_at') DESC,id DESC);
CREATE INDEX messages_thread_time ON messages(json_extract(data,'$.thread_id'),json_extract(data,'$.created_at') DESC,id DESC);
CREATE INDEX events_task_sequence ON events(task_id,sequence);
CREATE INDEX events_source_sequence ON events(source_id,sequence);
CREATE INDEX update_feed_task_sequence ON update_feed(task_id,sequence);
CREATE INDEX update_feed_source_sequence ON update_feed(source_id,sequence);
CREATE INDEX update_feed_topic_sequence ON update_feed(topic,sequence);
`
