// The Build Output dialog (#264): one per tab, mounted by the signed-in
// layout (BuildOutputDialog), so it stays open when the job panel that
// opened it goes away (a stack's job tray drops a job when it ends). It
// follows the job's own event stream, so it shows the output from the
// start (the server keeps the newest 500 events).

export interface ShownBuild {
	jobId: string;
	/** What runs, e.g. "Build Images of Silo". */
	title: string;
}

class BuildOutput {
	shown = $state<ShownBuild | null>(null);

	show(jobId: string, title: string) {
		this.shown = { jobId, title };
	}

	close() {
		this.shown = null;
	}
}

export const buildOutput = new BuildOutput();
