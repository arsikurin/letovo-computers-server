DROP TABLE IF EXISTS users CASCADE;
DROP TABLE IF EXISTS slots CASCADE;

CREATE TABLE IF NOT EXISTS users
(
    id    VARCHAR(20) UNIQUE NOT NULL,
    login TEXT               NOT NULL DEFAULT '',
    PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS slots
(
    id       CHAR(5) UNIQUE NOT NULL,
    is_taken BOOLEAN        NOT NULL DEFAULT FALSE,
    taken_by VARCHAR(20)    NOT NULL,
    PRIMARY KEY (id),
    FOREIGN KEY (taken_by) REFERENCES users (id)
);

INSERT INTO users (id, login)
VALUES ('null', '');
