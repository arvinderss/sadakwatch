# Vision Service

Boundary for road-condition computer vision.

Initial target classes:

- pothole
- road_crack
- broken_surface
- waterlogging
- debris
- road_edge_damage
- missing_lane_marking
- damaged_manhole
- speed_breaker

The vision service will return detections with class, confidence, bounding box, frame timestamp, and optional tracking ID. GPS and vehicle telemetry are attached by the edge pipeline rather than embedded into the model contract.